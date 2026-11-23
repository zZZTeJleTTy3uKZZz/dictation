// Воркер рабочей машины: подобрать запись созвона и отдать её сервису.
//
// Порт meetdigest_watcher/main.py. Телемост пишет встречу сам — воркер лишь
// замечает готовый файл и отправляет. Кроме этого он ловит встречи в других
// программах: заметив, что микрофон взяла конференция, предлагает записать и
// пишет два канала порознь.
//
// Связь с сервисом постоянная (server-sent events): о готовности конспекта
// воркер узнаёт сразу, а не опросом.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/catcher"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/client"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/config"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/journal"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/scanner"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/sources"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/state"
)

const version = "0.1.0"

const (
	// heartbeatEvery — как часто отмечаться на сервисе.
	heartbeatEvery = 5 * time.Minute
	// reconnectAfter — сколько ждать перед новой попыткой открыть поток событий.
	reconnectAfter = 15 * time.Second
	// firstRetry и maxRetry — правило повторов отправки.
	//
	// ОБЩЕЕ С ТЕЛЕФОНОМ: не ушло — ждём полминуты, дальше удваиваем до
	// получаса. Раньше запись перепосылалась каждым обходом: при лежащем
	// сервисе это сотни бесполезных попыток за ночь, а в журнале — сотни
	// одинаковых строк, среди которых не видно настоящей причины.
	firstRetry = 30 * time.Second
	maxRetry   = 30 * time.Minute
)

func main() {
	envFile := flag.String("env-file", "", "файл настроек")
	flag.Parse()

	settings, err := config.Load(*envFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	log, closeLog, err := journal.Setup(settings.LogFile, "воркер")
	if err != nil {
		fmt.Fprintln(os.Stderr, "журнал не открылся:", err)
		os.Exit(2)
	}
	defer closeLog()

	if _, err := exeInPath("ffmpeg"); err != nil {
		log.Error("нужен ffmpeg в PATH — им из записи извлекается звуковая дорожка")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	worker := &Worker{
		settings: settings,
		api:      client.New(settings.APIURL, settings.APIKey),
		state:    state.NewStore(settings.StatePath),
		log:      log,
		name:     config.DeviceName(settings.DeviceName),
		clientID: machineID(settings.StatePath),
		retry:    firstRetry,
	}

	directories := worker.watchDirectories()
	log.Info("слежу за папками", "папки", directories)
	worker.scanner = scanner.New(
		directories,
		scanner.NewJournal(filepath.Join(config.Home(), "watcher-sent.json")),
		settings.Extensions,
		settings.MaxAgeHours,
	)

	go worker.scanLoop(ctx)
	go worker.heartbeatLoop(ctx)
	go worker.eventsLoop(ctx)

	if settings.CatchMeetings {
		// Ловец поднимается всегда, когда он не выключен насовсем: следить или
		// молчать — решает режим в настройках, и меняется он на ходу.
		hunter := catcher.New(worker.state, log)
		go hunter.Run(ctx)
		log.Info("ловец встреч поднят", "режим", prefs.Load("").Mode)
	}

	<-ctx.Done()
	log.Info("воркер остановлен")
}

// Worker — наблюдение и отправка.
type Worker struct {
	settings *config.Config
	api      *client.Client
	scanner  *scanner.Scanner
	state    *state.Store
	log      *slog.Logger
	name     string
	clientID string

	sent      int
	lastError string
	retry     time.Duration
	nextRetry time.Time
}

// watchDirectories — все папки под наблюдением.
//
// Своя папка записей попадает сюда всегда: ловец кладёт разговоры туда, и без
// обхода они остались бы лежать на диске никем не замеченными.
func (w *Worker) watchDirectories() []string {
	chosen := []string{}
	if w.settings.WatchDirs != "" && w.settings.WatchDirs != config.DefaultWatchDir {
		// Заданная переменная главнее настроек: разовый запуск с другой папкой
		// не должен переписывать выбор человека, но и подменяться им тоже.
		for _, part := range splitPaths(w.settings.WatchDirs) {
			chosen = append(chosen, config.Expand(part))
		}
	} else {
		chosen = sources.Load("").Directories()
	}

	own := prefs.LoadRecording("").Directory()
	for _, one := range chosen {
		if one == own {
			return chosen
		}
	}
	_ = os.MkdirAll(own, 0o755)
	return append(chosen, own)
}

// scanLoop — обход папок и отправка найденного.
func (w *Worker) scanLoop(ctx context.Context) {
	interval := time.Duration(w.settings.Interval * float64(time.Second))
	for {
		w.followSources()
		w.round(ctx)
		if err := w.note(); err != nil {
			w.log.Warn("состояние не записано", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// followSources — подхватить папки, выбранные в настройках, без перезапуска.
//
// Человек добавляет папку и ждёт, что записи оттуда поедут сейчас, а не после
// перезапуска, про который он и не знает. Читается маленький файл раз в обход —
// дешевле, чем один заход в папку за списком файлов.
func (w *Worker) followSources() {
	chosen := w.watchDirectories()
	if w.scanner.Watch(chosen) {
		w.log.Info("папки наблюдения обновлены", "папки", chosen)
	}
}

func (w *Worker) round(ctx context.Context) {
	if time.Now().Before(w.nextRetry) {
		// Рано: сервис недавно отказал, ждём назначенную паузу. Новые записи
		// тоже подождут — если сервис лежит, они всё равно не уйдут, а журнал
		// засорят.
		return
	}
	for _, candidate := range w.scanner.Scan(time.Now()) {
		w.log.Info("новая запись",
			"файл", filepath.Base(candidate.Path),
			"МБ", fmt.Sprintf("%.1f", float64(candidate.Size)/1048576))
		if err := w.send(ctx, candidate); err != nil {
			if ctx.Err() != nil {
				return
			}
			// Сервис недоступен: запись остаётся неотмеченной и уйдёт позже.
			w.retry = min(w.retry*2, maxRetry)
			w.nextRetry = time.Now().Add(w.retry)
			w.lastError = "сервис не принял запись: " + err.Error()
			w.log.Warn("сервис не принял запись",
				"повтор через", w.retry.String(), "err", err)
			return
		}
	}
}

func (w *Worker) send(ctx context.Context, candidate scanner.Candidate) error {
	title := scanner.TitleFromFilename(candidate.Path)
	notes := ""
	if chat := scanner.FindChatFile(candidate.Path); chat != "" {
		if raw, err := os.ReadFile(chat); err == nil {
			notes = string(raw)
		}
	}

	job, err := w.api.SendRecording(ctx, candidate.Path, title, notes)
	if err != nil {
		return err
	}
	if err := w.scanner.Accept(candidate); err != nil {
		w.log.Warn("отметка об отправке не записана", "err", err)
	}
	w.sent++
	w.lastError = ""
	// Получилось — отсчёт начинается заново: прошлая неудача была разовой.
	w.retry = firstRetry
	w.nextRetry = time.Time{}
	w.log.Info("отправлено", "файл", filepath.Base(candidate.Path), "задание", job.ID)
	return nil
}

func (w *Worker) note() error {
	// Отметка в общей тетради: по ней значок в лотке понимает, что наблюдение
	// живо. Пишется каждый круг, а не раз в пять минут вместе с сообщением
	// сервису: иначе значок половину времени показывал бы «воркер не
	// отвечает», хотя он работает.
	return w.state.Update(func(value *state.State) {
		value.Working = true
		value.SentCount = w.sent
		value.LastError = w.lastError
	})
}

// heartbeatLoop — отмечаться на сервисе.
func (w *Worker) heartbeatLoop(ctx context.Context) {
	for {
		online := true
		err := w.api.Heartbeat(ctx, client.Device{
			ClientID:  w.clientID,
			Name:      w.name,
			Kind:      "watcher",
			Platform:  runtime.GOOS + " " + osRelease(),
			Version:   version,
			Watching:  w.scanner.Directories(),
			SentCount: w.sent,
			LastError: w.lastError,
		})
		if err != nil {
			online = false
			if ctx.Err() != nil {
				return
			}
			w.log.Warn("не удалось отметиться на сервисе", "err", err)
		}
		// Есть ли связь — знает только тот, кто в неё ходил.
		if err := w.state.Update(func(value *state.State) { value.Online = online }); err != nil {
			w.log.Warn("состояние не записано", "err", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(heartbeatEvery):
		}
	}
}

// eventsLoop — держать поток событий и рассказывать, что происходит с заданиями.
func (w *Worker) eventsLoop(ctx context.Context) {
	for {
		err := w.api.Events(ctx, w.onEvent)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// ЛОВИМ ВСЁ, А НЕ ТОЛЬКО ОШИБКИ СЕТИ. 03.09.2026 Python-версия
			// умерла целиком от оборванного TLS: ошибка не относилась к
			// «сетевым» и пролетала мимо перехвата, процесс вышел, а
			// планировщик счёл задачу отработавшей штатно — наблюдение молча
			// стояло сорок минут.
			w.log.Warn("связь с сервисом потеряна", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectAfter):
		}
	}
}

func (w *Worker) onEvent(event client.Event) {
	switch event.Name {
	case "ready":
		jobs, _ := event.Payload["jobs"].(float64)
		w.log.Info("связь с сервисом установлена", "заданий на виду", int(jobs))
	case "job.created", "job.status":
		key, title := event.Stage()
		// В журнал — словами сервиса, а не сырым состоянием: «processing»
		// ничего не говорит, «Распознаю речь» — говорит, и теми же словами,
		// что видит телефон.
		jobID, _ := event.Payload["job_id"].(string)
		w.log.Info("задание", "id", jobID, "стадия", title)
		if key == "done" || key == "partial" {
			name, _ := event.Payload["title"].(string)
			if name == "" {
				name = "Разбор"
			}
			if err := w.state.Update(func(value *state.State) {
				value.ReadyTitle = name
				value.ReadyAt = float64(time.Now().Unix())
			}); err != nil {
				w.log.Warn("состояние не записано", "err", err)
			}
		}
	}
}

// ── мелочи ────────────────────────────────────────────────────────────────

func splitPaths(raw string) []string {
	found := []string{}
	for _, part := range filepath.SplitList(raw) {
		if part != "" {
			found = append(found, part)
		}
	}
	if len(found) == 0 {
		// В watcher.env папки разделяют точкой с запятой — так это описано в
		// README и так лежит у людей.
		found = append(found, splitSemicolon(raw)...)
	}
	return found
}

func splitSemicolon(raw string) []string {
	found := []string{}
	current := ""
	for _, symbol := range raw {
		if symbol == ';' {
			if current != "" {
				found = append(found, current)
			}
			current = ""
			continue
		}
		current += string(symbol)
	}
	if current != "" {
		found = append(found, current)
	}
	return found
}

// machineID — стабильный признак этой машины.
//
// Нужен, чтобы сервис узнавал компьютер после переименования: имя человек
// меняет в настройках, и без такого признака в списке устройств появлялась бы
// вторая запись того же компьютера.
func machineID(statePath string) string {
	path := filepath.Join(filepath.Dir(statePath), "device-id")
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		return string(raw)
	}
	id := fmt.Sprintf("%d-%s", time.Now().UnixNano(), config.DeviceName(""))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		_ = os.WriteFile(path, []byte(id), 0o644)
	}
	return id
}

func osRelease() string {
	// Точную сборку Windows здесь не выясняем: сервису достаточно знать, что
	// это windows, а версия видна в журнале устройства.
	return ""
}

func exeInPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", errors.New(name + " не найден в PATH")
	}
	return path, nil
}

func min(one, other time.Duration) time.Duration {
	if one < other {
		return one
	}
	return other
}
