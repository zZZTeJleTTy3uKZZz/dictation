// Значок MeetDigest в лотке.
//
// ГЛАВНОЕ ПРАВИЛО: на нажатие — ничего тяжёлого. Меню собирается из того, что
// уже лежит на диске (тетрадь состояния и список встреч), а за новыми данными
// ходит отдельная нить в своём темпе. Python-версия спрашивала сервис прямо в
// обработчике нажатия, и меню открывалось три секунды — выглядело это как
// зависший значок.
//
// ПОЧЕМУ ОТДЕЛЬНАЯ ПРОГРАММА. Значок живёт в сеансе человека и должен умирать
// вместе с ним, а наблюдение — в задаче планировщика и должно жить дальше.
// Общаются они через тетрадь состояния: значок просит, воркер делает.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/client"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/config"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/dictaphone"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hidden"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/journal"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/state"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/trayupdate"
)

// scoutEvery — как часто нить обновляет список встреч.
//
// Минута: конспект готовится минутами, и чаще спрашивать нечего. Человек всё
// равно увидит свежий список в тот же миг, как откроет меню, — он уже на диске.
const scoutEvery = time.Minute

// meetingsShown — сколько встреч влезает в меню, не превращая его в простыню.
const meetingsShown = 5

// Tray — значок и всё, что он показывает.
type Tray struct {
	log   *slog.Logger
	store *state.Store
	api   *client.Client

	guard    sync.Mutex
	meetings []client.Meeting
	alive    bool

	// voice — диктофон заметок; nil, если человек его выключил.
	voice *dictaphone.Dictaphone
	// hotkeyTrouble — почему диктовка по клавише не работает; пусто, если всё
	// хорошо. Держим здесь, чтобы меню сказало человеку то же, что и журнал.
	hotkeyTrouble string
	// dictation — диктовка: текст в то поле, где стоит курсор.
	dictation Dictation
	// device — как машина подписана: с этим именем заметка попадает в историю,
	// потому что «откуда это» — половина ответа на «что это было».
	device string

	// upd — служба обновления; в тестах не нужна, значок ходит к ней сам.
	upd trayupdate.Service
	// update — последний ответ службы про обновление и занятость проверки.
	update updateMemory
}

func main() {
	log, closeLog, err := journal.Setup(filepath.Join(config.Home(), "tray.log"), "значок")
	if err != nil {
		fmt.Fprintln(os.Stderr, "журнал:", err)
		os.Exit(1)
	}
	defer closeLog()

	settings, err := config.Load("")
	if err != nil {
		// Без ключа значок показывать нечего, но и падать молча нельзя: человек
		// должен увидеть причину в журнале.
		log.Error("настройки", "err", err)
		os.Exit(1)
	}

	tray := &Tray{
		log:    log,
		store:  state.NewStore(settings.StatePath),
		api:    client.New(settings.APIURL, settings.APIKey),
		device: config.DeviceName(settings.DeviceName),
		upd:    localUpdates{},
	}
	// Встречи, собранные прошлым запуском: меню должно быть полным с первой же
	// секунды, а не через минуту после входа в систему.
	tray.meetings = readCache()

	systray.Run(tray.start, func() { log.Info("значок закрыт") })
}

func (t *Tray) start() {
	systray.SetTitle("MeetDigest")
	systray.SetTooltip(trayTip)
	systray.SetIcon(iconFor("idle"))

	ctx, stop := context.WithCancel(context.Background())
	go t.scout(ctx)
	go t.watchState(ctx)
	go t.pollUpdate(ctx)

	// Диктофон поднимается здесь, а не в воркере: клавиатура есть только у
	// программы, которая живёт в сеансе человека.
	t.voice = t.startVoice(ctx, t.device)

	// Ядро диктовки и плашка поднимаются сразу и ждут: нажатие клавиши должно
	// давать плашку мгновенно, а не через пять секунд на запуск двух программ.
	if err := t.dictation.Rise(t.log); err != nil {
		t.log.Error("диктовка не поднялась", "err", err)
	}

	t.build(ctx, stop)
}

// build — собрать меню один раз.
//
// Пункты меняются подписью, а не пересборкой: systray на Windows пересоздаёт
// меню целиком, и перестройка на каждое изменение мигала бы у человека под
// курсором.
func (t *Tray) build(ctx context.Context, stop context.CancelFunc) {
	status := systray.AddMenuItem("…", "")
	status.Disable()

	// Строка про сломанную клавишу — первой в меню: человек открывает меню
	// именно тогда, когда что-то не работает. Пока всё хорошо, её не видно.
	trouble := systray.AddMenuItem("", "")
	if t.hotkeyTrouble == "" {
		trouble.Hide()
	} else {
		trouble.SetTitle(t.hotkeyTrouble)
		trouble.SetTooltip("диктовка по клавише не работает: сочетание занято")
		trouble.Disable()
	}
	systray.AddSeparator()

	record := systray.AddMenuItem("Записать этот разговор", "записать то, что идёт сейчас")
	stopRecord := systray.AddMenuItem("Остановить запись", "дописать и отправить")
	stopRecord.Hide()
	systray.AddSeparator()

	dictate := systray.AddMenuItem("Диктовать в поле", "текст появится там, где курсор")
	note := systray.AddMenuItem("Записать заметку", "заметка голосом, без вставки в поле")
	if t.voice == nil {
		note.Hide()
	}
	notesList := systray.AddMenuItem("Мои заметки", "вся история заметок")
	systray.AddSeparator()

	meetings := systray.AddMenuItem("Последние встречи", "")
	meetings.Disable()
	rows := make([]*systray.MenuItem, meetingsShown)
	for index := range rows {
		rows[index] = systray.AddMenuItem("", "")
		rows[index].Hide()
	}
	systray.AddSeparator()

	quiet := systray.AddMenuItem("Не спрашивать час", "молчать про встречи")
	settings := systray.AddMenuItem("Настройки", "папки, режим, качество")
	systray.AddSeparator()
	// Обновление: строка статуса — только когда есть что сказать; пункт
	// установки — только когда есть версия. «Проверить» виден всегда.
	updateLine := systray.AddMenuItem("", "")
	updateLine.Disable()
	updateLine.Hide()
	install := systray.AddMenuItem("", "поставить найденную версию Диктовки")
	install.Hide()
	check := systray.AddMenuItem("Проверить обновление", "поискать новую версию Диктовки")
	update := &updateMenu{line: updateLine, install: install, check: check}
	systray.AddSeparator()
	quit := systray.AddMenuItem("Выйти", "закрыть значок")
	// Красного текста в системном меню Windows не бывает, поэтому опасный пункт
	// помечается красным значком — единственным цветным в меню.
	quit.SetIcon(redDot())

	go t.refresh(status, record, stopRecord, meetings, rows, quiet, note, dictate, update)

	for {
		select {
		case <-record.ClickedCh:
			t.ask(func(value *state.State) { value.StartRequested = true })
		case <-stopRecord.ClickedCh:
			t.ask(func(value *state.State) { value.StopRequested = true })
		case <-dictate.ClickedCh:
			go t.toggleDictation()
		case <-note.ClickedCh:
			if t.voice != nil {
				// Нажатие в меню делает то же, что сочетание клавиш: одно
				// поведение, один путь в коде.
				go t.voice.Toggle(ctx)
			}
		case <-notesList.ClickedCh:
			t.openWindow("notes")
		case <-quiet.ClickedCh:
			t.toggleQuiet()
		case <-settings.ClickedCh:
			t.openWindow("settings")
		case <-check.ClickedCh:
			go t.checkUpdate(ctx)
		case <-install.ClickedCh:
			go t.installUpdate(ctx)
		case <-quit.ClickedCh:
			stop()
			t.dictation.Quit()
			if t.voice != nil {
				// Дописываем начатую заметку, а не бросаем её: человек говорил,
				// и потерять это нельзя.
				finish, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				t.voice.Finish(finish)
				t.voice.Wait(finish)
				cancel()
			}
			systray.Quit()
			return
		case item := <-t.meetingClicks(rows):
			t.openMeeting(item)
		}
	}
}

// meetingClicks — нажатия на любую строку встречи одним каналом.
func (t *Tray) meetingClicks(rows []*systray.MenuItem) <-chan int {
	clicks := make(chan int, len(rows))
	for index, row := range rows {
		go func(index int, row *systray.MenuItem) {
			for range row.ClickedCh {
				clicks <- index
			}
		}(index, row)
	}
	return clicks
}

// refresh — держать подписи в меню свежими.
func (t *Tray) refresh(status, record, stopRecord, heading *systray.MenuItem,
	rows []*systray.MenuItem, quiet, note, dictate *systray.MenuItem, menu *updateMenu) {
	for {
		value := t.store.Read()
		status.SetTitle(statusLine(value))

		// Пока идёт запись, предлагать начать ещё одну бессмысленно.
		if value.LiveRecording() {
			record.Hide()
			stopRecord.Show()
			stopRecord.SetTitle("Остановить запись" + since(value))
		} else {
			stopRecord.Hide()
			record.Show()
		}

		t.guard.Lock()
		known := append([]client.Meeting(nil), t.meetings...)
		t.guard.Unlock()

		if len(known) == 0 {
			heading.SetTitle("Разобранных встреч пока нет")
		} else {
			heading.SetTitle("Последние встречи")
		}
		for index, row := range rows {
			if index >= len(known) {
				row.Hide()
				continue
			}
			row.SetTitle(meetingLine(known[index]))
			row.Show()
		}

		if line, show := voiceLine(t.voice); show {
			note.SetTitle(line)
			note.Show()
		}
		if t.dictation.Going() {
			dictate.SetTitle("Закончить диктовку (" + currentHotkey() + ")")
		} else {
			dictate.SetTitle("Диктовать в поле (" + currentHotkey() + ")")
		}

		t.paintUpdate(menu)

		if prefs.Load("").Silent(time.Now()) {
			quiet.SetTitle("Снова спрашивать про встречи")
		} else {
			quiet.SetTitle("Не спрашивать час")
		}

		time.Sleep(2 * time.Second)
	}
}

// watchState — менять сам значок по состоянию.
func (t *Tray) watchState(ctx context.Context) {
	shown := ""
	for {
		badge := t.store.Read().Badge()
		// Заметка обновления — часть значка, а не отдельное состояние: меняется
		// только картинка, а не то, что значок показывает про запись.
		dot := t.updateBadge()
		key := badge
		if dot {
			key += "+обновление"
		}
		if key != shown && prefs.LoadDisplay("").Tray {
			systray.SetIcon(iconWithUpdate(badge, dot))
			shown = key
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// scout — нить, которая ходит в сеть. Нажатие на значок её не ждёт.
func (t *Tray) scout(ctx context.Context) {
	for {
		t.collect(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(scoutEvery):
		}
	}
}

func (t *Tray) collect(ctx context.Context) {
	ask, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	found, err := t.api.Meetings(ask, meetingsShown)
	if err != nil {
		// Сеть пропала — меню остаётся с прошлым списком: устаревший список
		// полезнее пустого.
		t.log.Debug("список встреч не обновился", "err", err)
		t.guard.Lock()
		t.alive = false
		t.guard.Unlock()
		return
	}

	t.guard.Lock()
	t.meetings = found
	t.alive = true
	t.guard.Unlock()
	writeCache(found)
}

func (t *Tray) ask(change func(*state.State)) {
	if err := t.store.Ask(change); err != nil {
		t.log.Error("просьба не записалась", "err", err)
	}
}

func (t *Tray) toggleQuiet() {
	settings := prefs.Load("")
	if settings.Silent(time.Now()) {
		settings.Speak()
	} else {
		settings.BeQuiet(time.Now())
	}
	if err := settings.Save(); err != nil {
		t.log.Error("тихий час не записался", "err", err)
	}
}

// openWindow — поднять окно приложения отдельным процессом.
//
// Окно одно на всё: настройки и заметки — его разделы, а не отдельные программы.
// Второе открытие поднимает второе окно — это лучше, чем молча ничего не делать,
// когда человек нажал и ждёт.
func (t *Tray) openWindow(kind string) {
	self, err := os.Executable()
	if err != nil {
		t.log.Error("не найти себя на диске", "err", err)
		return
	}
	beside := filepath.Dir(self)
	command := hidden.Command(filepath.Join(beside, "meetdigest-ui.exe"))
	if kind == "notes" {
		command = hidden.Command(filepath.Join(beside, "meetdigest-ui.exe"), "--notes")
	}
	if err := command.Start(); err != nil {
		t.log.Error("окно не открылось", "err", err)
		return
	}
	// Ждать окно нельзя — значок замрёт на всё время, пока человек в настройках.
	go func() { _ = command.Wait() }()
}

// openMeeting — открыть разбор встречи в браузере.
func (t *Tray) openMeeting(index int) {
	t.guard.Lock()
	known := append([]client.Meeting(nil), t.meetings...)
	t.guard.Unlock()
	if index >= len(known) {
		return
	}
	settings, err := config.Load("")
	if err != nil || settings.APIURL == "" {
		t.log.Error("адрес сервиса не задан — встречу открыть нельзя", "err", err)
		return
	}
	address := fmt.Sprintf("%s/meetings/%s", strings.TrimRight(settings.APIURL, "/"), known[index].JobID)
	if err := hidden.Command("rundll32", "url.dll,FileProtocolHandler", address).Start(); err != nil {
		t.log.Error("встреча не открылась", "err", err)
	}
}

// ── подписи ───────────────────────────────────────────────────────────────

func statusLine(value state.State) string {
	switch {
	case value.LiveRecording():
		return "Идёт запись: " + value.RecordingTitle
	case !value.Fresh():
		return "Наблюдение не запущено"
	case !value.Online:
		return "Нет связи с сервисом"
	default:
		return fmt.Sprintf("Наблюдаю · отправлено: %d", value.SentCount)
	}
}

// since — сколько уже пишем.
func since(value state.State) string {
	if value.RecordingSince == 0 {
		return ""
	}
	passed := time.Since(time.Unix(int64(value.RecordingSince), 0))
	return fmt.Sprintf(" (%d:%02d)", int(passed.Minutes()), int(passed.Seconds())%60)
}

func meetingLine(meeting client.Meeting) string {
	line := meeting.Title
	if meeting.Emoji != "" {
		line = meeting.Emoji + " " + line
	}
	if meeting.Tasks > 0 {
		line += fmt.Sprintf(" · задач: %d", meeting.Tasks)
	}
	return line
}
