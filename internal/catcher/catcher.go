// Package catcher — следит за разговорами и записывает те, что человек разрешил.
//
// Порт meetdigest_watcher/catcher.py.
//
// КАК ЭТО РАБОТАЕТ. Раз в несколько секунд смотрим, не взяла ли микрофон
// конференция. Взяла — решаем по настройкам: писать сразу, спросить плашкой
// или молчать. Разговор кончился (микрофон свободен дольше паузы) — дописываем
// запись и кладём её в папку, откуда её подберёт обход.
//
// ПОЧЕМУ ПАУЗА ПЕРЕД КОНЦОМ. Микрофон освобождается и на секунду: человек
// переключил устройство, программа перезапустила поток. Останавливать запись
// по первому же молчанию значило бы резать встречу на куски.
package catcher

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hidden"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/micuse"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/state"
)

const (
	// LookEvery — как часто смотреть на микрофон.
	//
	// Три секунды: предложение записать должно появиться в начале разговора, а
	// не к его середине, и при этом чтение реестра стоит почти ничего.
	LookEvery = 3 * time.Second
	// SilenceBeforeEnd — сколько микрофон должен быть свободен, чтобы считать
	// разговор законченным.
	SilenceBeforeEnd = 20 * time.Second
	// AskFor — сколько плашка ждёт ответа.
	//
	// Минута: не ответили — значит человек занят разговором, и переспрашивать
	// не надо. Запись при этом не начинается: молча писать разговор нехорошо.
	AskFor = time.Minute
)

// Tape — то, на что пишется разговор.
//
// За интерфейсом стоит recorder.Recorder; он нужен, чтобы проверки не лезли в
// настоящую звуковую карту: на машине сборки её нет вовсе, а на рабочей машине
// проверка посреди дня открывала бы микрофон без спроса.
type Tape interface {
	Start() error
	Stop(title string) (string, error)
}

// Catcher — ловец встреч.
type Catcher struct {
	state *state.Store
	log   *slog.Logger

	// Look — чем смотреть на микрофон; подменяется в проверках.
	Look func() *micuse.Meeting
	// Ask — чем спрашивать человека. Возвращает ответ плашки.
	Ask func(ctx context.Context, meeting *micuse.Meeting) string
	// Now — текущее время; в проверках его двигают руками.
	Now func() time.Time
	// Open — чем писать разговор.
	Open func(settings *prefs.Recording, folder string) Tape

	current  *micuse.Meeting
	refused  map[string]bool
	free     time.Time
	recorder Tape
	started  time.Time
}

func New(store *state.Store, log *slog.Logger) *Catcher {
	made := &Catcher{
		state:   store,
		log:     log,
		Look:    micuse.Look,
		Now:     time.Now,
		refused: map[string]bool{},
	}
	made.Ask = made.askByWindow
	made.Open = func(settings *prefs.Recording, folder string) Tape {
		return openRecorder(settings, folder, log)
	}
	return made
}

// Run — следить, пока не погасят.
func (c *Catcher) Run(ctx context.Context) {
	// При старте мы точно ничего не пишем. Если в общей тетради осталась
	// запись от прежнего запуска — она умерла вместе с тем процессом, и значок
	// иначе показывал бы её вечно, а остановить было бы нечем.
	if c.state.Read().Recording {
		c.log.Info("сбрасываю запись, оставшуюся от прошлого запуска")
		_ = c.state.Update(func(value *state.State) {
			value.Recording = false
			value.RecordingTitle = ""
			value.RecordingSince = 0
		})
	}

	for {
		c.Round(ctx)
		select {
		case <-ctx.Done():
			c.finish(ctx) // дописываем начатое, а не бросаем на середине
			return
		case <-time.After(LookEvery):
		}
	}
}

// Round — один круг наблюдения. Вынесен наружу, чтобы проверки не ждали.
func (c *Catcher) Round(ctx context.Context) {
	// Просьбы из значка важнее наблюдения: человек нажал и ждёт.
	c.serveRequests(ctx)

	meeting := c.Look()
	if meeting != nil {
		c.onMeeting(ctx, meeting)
		return
	}
	c.onSilence(ctx)
}

// serveRequests — выполнить то, о чём попросил значок в лотке.
func (c *Catcher) serveRequests(ctx context.Context) {
	asked := c.state.Read()
	switch {
	case asked.StopRequested && c.recorder != nil:
		_ = c.state.Ask(func(value *state.State) { value.StopRequested = false })
		c.log.Info("остановить запись попросили из лотка")
		c.finish(ctx)
	case asked.StartRequested && c.recorder == nil:
		_ = c.state.Ask(func(value *state.State) { value.StartRequested = false })
		if meeting := c.Look(); meeting != nil {
			// Запись по просьбе идёт даже для запрещённых программ: запрет
			// означает «не предлагай сам», а не «не давай записать, когда я
			// прошу».
			c.log.Info("записать попросили из лотка", "встреча", meeting.Label())
			c.begin(meeting)
		}
	}
}

func (c *Catcher) onMeeting(ctx context.Context, meeting *micuse.Meeting) {
	c.free = time.Time{}
	key := c.key(meeting)

	if c.recorder != nil {
		return // уже пишем эту встречу
	}
	if c.current != nil && c.key(c.current) == key {
		return // уже знаем про неё
	}
	if c.refused[key] {
		return // человек отказался, второй раз не спрашиваем
	}

	// Решения могли поменяться со стороны — перечитываем, а не помним навсегда.
	settings := prefs.Load("")
	if !settings.Offers(meeting.Program, meeting.Tab, meeting.Host, meeting.Address) {
		return // запрещено списком или не разрешено белым списком
	}
	if settings.Silent(c.Now()) {
		return // попросили помолчать
	}

	c.current = meeting
	c.log.Info("замечена встреча", "встреча", meeting.Label(), "программа", meeting.Program)

	if settings.Demands(meeting.Program, meeting.Host, meeting.Address) {
		// «Всегда записывать»: спрашивать нечего, начинаем и показываем отметку.
		c.begin(meeting)
		return
	}

	answer := c.Ask(ctx, meeting)
	c.apply(ctx, meeting, answer)
}

// apply — что делать с ответом плашки.
func (c *Catcher) apply(ctx context.Context, meeting *micuse.Meeting, answer string) {
	settings := prefs.Load("")
	switch answer {
	case "record":
		c.begin(meeting)
		return
	case "always":
		// «Всегда» для браузера — это встреча или сайт, а не Chrome целиком:
		// разрешив программу, человек разрешил бы заодно музыку и видео.
		switch {
		case meeting.Address != "":
			settings.AllowPage(meeting.Address)
		case meeting.Host != "":
			settings.AllowSite(meeting.Host)
		default:
			settings.AllowAlways(meeting.Program)
		}
		c.save(settings)
		c.begin(meeting)
		return
	case "always-site":
		settings.AllowSite(meeting.Host)
		c.save(settings)
		c.begin(meeting)
		return
	case "never":
		// Для браузера «никогда» относится к этой встрече, а не ко всему
		// браузеру: в нём же проходят и нужные созвоны. Адресуем решение
		// ссылкой, а не названием вкладки: название сайт меняет сам (счётчик
		// сообщений, имя докладчика), и запрет молча переставал действовать.
		switch {
		case meeting.Address != "":
			settings.ForbidPage(meeting.Address)
		case meeting.Host != "":
			settings.ForbidSite(meeting.Host)
		default:
			settings.Forbid(meeting.Program)
		}
		c.save(settings)
		c.log.Info("больше не предлагаю записывать", "встреча", meeting.Label())
	case "never-site":
		settings.ForbidSite(meeting.Host)
		c.save(settings)
		c.log.Info("больше не предлагаю записывать сайт", "сайт", meeting.Host)
	case "never-app":
		settings.Forbid(meeting.Program)
		c.save(settings)
		c.log.Info("больше не предлагаю записывать программу", "программа", meeting.Program)
	case "quiet":
		settings.BeQuiet(c.Now())
		c.save(settings)
		c.log.Info("молчу час")
	}

	// Плашка закрылась сама или её сняли: если запись не начиналась — это отказ.
	if c.recorder == nil {
		c.refused[c.key(meeting)] = true
		c.current = nil
	}
	_ = ctx
}

func (c *Catcher) save(settings *prefs.Prefs) {
	if err := settings.Save(); err != nil {
		c.log.Warn("решение не записано", "err", err)
	}
}

// begin — начать запись.
func (c *Catcher) begin(meeting *micuse.Meeting) {
	settings := prefs.LoadRecording("")
	folder, err := settings.Target()
	if err != nil {
		c.log.Error("папка записей недоступна", "err", err)
		return
	}

	// Настройки читаются в момент старта, а не при запуске воркера: человек
	// включает запись экрана перед той встречей, где будут что-то показывать,
	// и ждёт, что это подействует сейчас.
	made := c.Open(settings, folder)
	if err := made.Start(); err != nil {
		c.log.Error("запись не началась", "err", err)
		return
	}
	c.recorder = made
	c.current = meeting
	c.started = c.Now()

	// Значок в лотке узнаёт о записи отсюда: у него своя жизнь и свой процесс.
	if err := c.state.Update(func(value *state.State) {
		value.Recording = true
		value.RecordingTitle = meeting.Label()
		value.RecordingSince = float64(c.started.Unix())
	}); err != nil {
		c.log.Warn("состояние не записано", "err", err)
	}
	c.log.Info("пишу встречу", "встреча", meeting.Label())
}

func (c *Catcher) onSilence(ctx context.Context) {
	if c.current == nil && c.recorder == nil {
		return
	}
	if c.free.IsZero() {
		c.free = c.Now()
		return
	}
	if c.Now().Sub(c.free) < SilenceBeforeEnd {
		return // микрофон мог освободиться на секунду — это не конец встречи
	}
	c.finish(ctx)
}

// finish — дописать запись и убрать её в папку.
func (c *Catcher) finish(ctx context.Context) {
	defer func() {
		c.current = nil
		c.free = time.Time{}
	}()
	if c.recorder == nil {
		return
	}

	title := "Разговор"
	if c.current != nil {
		title = c.current.Label()
	}
	title = safeName(title + " " + c.started.Format("02.01.06 15-04"))

	made, err := c.recorder.Stop(title)
	c.recorder = nil
	if err != nil {
		c.log.Error("запись не сохранилась", "err", err)
	} else if made != "" {
		c.log.Info("запись готова", "файл", filepath.Base(made))
	}

	if err := c.state.Update(func(value *state.State) {
		value.Recording = false
		value.RecordingTitle = ""
		value.RecordingSince = 0
	}); err != nil {
		c.log.Warn("состояние не записано", "err", err)
	}
	_ = ctx
}

// key — чем отличается одна встреча от другой.
//
// Программа плюс вкладка: в браузере это разные встречи, и отказ от одной не
// должен молчать про другую.
func (c *Catcher) key(meeting *micuse.Meeting) string {
	return meeting.Program + "|" + meeting.Address + "|" + meeting.Tab
}

// askByWindow — спросить человека плашкой.
//
// Плашка — отдельный процесс: у неё свой оконный цикл, и падение окна не
// должно уносить наблюдение. Ответ приходит одной строкой в стандартный вывод.
func (c *Catcher) askByWindow(ctx context.Context, meeting *micuse.Meeting) string {
	display := prefs.LoadDisplay("")
	if !display.Window {
		// Человек предпочёл не видеть окон: спрашивать нечем, а писать без
		// спроса нельзя — считаем это отказом до следующей встречи.
		return ""
	}

	self, err := ownExecutable()
	if err != nil {
		c.log.Warn("не найти себя на диске", "err", err)
		return ""
	}
	window := filepath.Join(filepath.Dir(self), "meetdigest-window.exe")

	ask, cancel := context.WithTimeout(ctx, AskFor)
	defer cancel()
	command := hidden.CommandContext(ask, window,
		"-kind", "ask",
		"-title", meeting.Label(),
		"-program", meeting.Title,
		"-host", meeting.Host,
	)
	out, err := command.Output()
	if err != nil {
		// Плашка закрылась без ответа или окно не поднялось — это отказ.
		return ""
	}
	return trimLine(string(out))
}
