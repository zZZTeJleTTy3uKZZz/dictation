// Диктовка: говорю — текст появляется в поле, где стоит курсор.
//
// ПОЧЕМУ ЭТО ДОЛГОЖИВУЩАЯ ПРОГРАММА, А НЕ ЗАПУСК НА КАЖДУЮ ДИКТОВКУ. Модель
// распознавания поднимается четыре секунды. Запускать её по нажатию клавиши —
// значит четыре секунды молчать в ответ на действие, которое человек делает
// ровно затем, чтобы сэкономить время. Поэтому ядро запускается один раз вместе
// со значком, греет модель и ждёт команды.
//
//	meetdigest-core dictate
//
// Со стандартного ввода принимает по слову в строке:
//
//	start   — начать диктовку
//	stop    — закончить и оставить текст
//	cancel  — закончить и не сохранять
//	quit    — выйти
//
// В стандартный вывод пишет по строке JSON на каждое событие: их читает плашка.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/config"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/dictation"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/notes"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/recorder"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/typing"
)

// Dictaphone — ядро диктовки: ждёт команд и ведёт по одной диктовке за раз.
type Dictaphone struct {
	log   *slog.Logger
	voice *chosen

	mu      sync.Mutex
	running *dictation.Dictation
	stop    func()
	dropped bool
}

func dictate() int {
	settings := prefs.LoadVoice("")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	made := &Dictaphone{log: log, voice: speechFor(settings)}

	say(dictation.Event{Stage: "готов"})

	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		switch strings.TrimSpace(reader.Text()) {
		case "start":
			made.begin()
		case "stop":
			made.finish(false)
		case "cancel":
			made.finish(true)
		case "quit":
			made.finish(false)
			return 0
		}
	}
	// Ввод закрылся — значок умер; дописываем начатое и уходим.
	made.finish(false)
	return 0
}

// begin — начать диктовку.
func (d *Dictaphone) begin() {
	d.mu.Lock()
	if d.running != nil {
		d.mu.Unlock()
		return
	}
	d.dropped = false
	d.mu.Unlock()

	// Окно, куда печатать, запоминаем СЕЙЧАС: пока человек говорит, он может
	// переключиться, а текст обязан уйти туда, где диктовка началась.
	target, title := typing.Foreground()
	if title != "" {
		fmt.Fprintln(os.Stderr, "печатаю в окно:", title)
	}

	folder := filepath.Join(config.Home(), "dictation")
	tape := recorder.NewSpeech(folder, d.log)
	hands := &typist{target: target}
	made := dictation.New(&tapeAdapter{tape: tape}, d.voice, hands, d.log)

	ctx, stop := context.WithCancel(context.Background())
	events, err := made.Run(ctx)
	if err != nil {
		stop()
		say(dictation.Event{Stage: dictation.StageTrouble, Trouble: err.Error()})
		say(dictation.Event{Stage: "скрыть"})
		return
	}

	d.mu.Lock()
	d.running, d.stop = made, stop
	d.mu.Unlock()

	began := time.Now()
	go func() {
		whole := ""
		for one := range events {
			sayLater(one)
			if one.Whole != "" {
				whole = one.Whole
			}
		}
		stop()

		d.mu.Lock()
		dropped := d.dropped
		d.running, d.stop = nil, nil
		d.mu.Unlock()

		// Набрать не вышло — не теряем сказанное: весь текст целиком уходит в
		// буфер обмена, и человеку говорится, что он там. Отменённую диктовку не
		// трогаем: он выбросил её нарочно.
		trouble := ""
		if why := hands.trouble(); why != "" && whole != "" && !dropped {
			if err := typing.Copy(whole); err != nil {
				trouble = fmt.Sprintf("вставить не вышло (%s), и в буфер обмена не легло: %s", why, err)
			} else {
				trouble = fmt.Sprintf("вставить не вышло (%s) — текст в буфере обмена", why)
			}
			say(dictation.Event{Stage: dictation.StageTrouble, Trouble: trouble, Whole: whole})
		}

		// Диктовка попадает в историю: человек потом ищет «что я тогда
		// наговорил», не разбирая, была это заметка или диктовка. Кроме
		// отменённой — её он выбросил нарочно.
		//
		// Способ расшифровки пишем тот, которым речь и разобрали: выбранный
		// мог оказаться неготовым и подмениться рабочим, а история должна
		// говорить правду о том, чей это текст.
		if whole != "" && !dropped {
			store := notes.NewStore(notes.DefaultPath(config.Home()))
			if err := store.Add(notes.Note{
				At:      began,
				Seconds: time.Since(began).Seconds(),
				Device:  config.DeviceName(""),
				Text:    whole,
				Speech:  d.voice.by(),
				Source:  notes.SourceWidget,
				Trouble: trouble,
			}); err != nil {
				fmt.Fprintln(os.Stderr, "в историю не попало:", err)
			}
		}
		// Плашке пора спрятаться: диктовка кончилась.
		say(dictation.Event{Stage: "скрыть", Whole: whole})
	}()
}

// finish — закончить диктовку; drop — не сохранять.
func (d *Dictaphone) finish(drop bool) {
	d.mu.Lock()
	running := d.running
	if drop {
		d.dropped = true
	}
	d.mu.Unlock()

	if running == nil {
		return
	}
	if drop {
		running.Drop()
	}
	_ = running.Stop()
}

// say — отдать событие плашке.
func say(one dictation.Event) {
	sayTo(выводПоток, one)
}

// sayTo — записать событие в поток.
func sayTo(поток io.Writer, one dictation.Event) {
	raw, err := json.Marshal(one)
	if err != nil {
		return
	}
	fmt.Fprintln(поток, string(raw))
}

// выводПоток — куда уходят события. Отдельной переменной, а не прямо в stdout,
// чтобы проверка могла подставить поток, который никто не читает.
var выводПоток io.Writer = os.Stdout

// sayLater — показать событие, не дожидаясь, пока его прочтут.
//
// ПОЧЕМУ НЕ ПРОСТО say. События читает тот, кто запустил ядро, и пока он читает
// — всё хорошо. Если он замешкался, труба заполняется, и `say` встаёт; вместе с
// ним встаёт весь разбор, а значит и учёт: сказанное не доходит ни до буфера
// обмена, ни до истории. Проверка на живом ноутбуке это и показала: текст
// печатался, а заметки не было. Терять сказанное из-за чужой медлительности
// недопустимо, поэтому события уходят в очередь отдельной нити, а переполняется
// очередь — теряются только показы, не слова.
//
// Последнее событие («скрыть») по-прежнему уходит напрямую: плашка обязана
// спрятаться, даже если очередь встала.
func sayLater(one dictation.Event) {
	выводРаз.Do(func() {
		// Поток запоминаем один раз, до запуска нити: иначе подмена потока в
		// проверке читалась бы из двух нитей сразу.
		поток := выводПоток
		go func() {
			for event := range вывод {
				sayTo(поток, event)
			}
		}()
	})
	select {
	case вывод <- one:
	default:
	}
}

var (
	// вывод — очередь событий для плашки; переполняется — показы теряются.
	вывод = make(chan dictation.Event, 256)
	// выводРаз — нить вывода одна на процесс.
	выводРаз sync.Once
)

// speechFor — чем распознавать куски. Местных моделей в диктовке больше нет:
// служба разбирает звук облаком, а остальные способы тоже облачные.
func speechFor(settings *prefs.Voice) *chosen {
	set := speech.NewSet(
		speech.NewDictationService(),
		speech.NewOpenAICompatible(speech.RemoteFromVoice(settings)),
	)
	return &chosen{set: set, name: settings.Speech}
}

type chosen struct {
	set  *speech.Set
	name string

	mu sync.Mutex
	// channel — чем разобрали последний кусок. Хранится затем, чтобы в историю
	// попало имя работавшего способа, а не выбранного: выбранный мог оказаться
	// неготовым, и разбирал другой (см. speech.Set.Choose).
	channel string
}

func (c *chosen) Text(ctx context.Context, path string) (string, error) {
	result, _, err := c.set.Text(ctx, c.name, path)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.channel = result.By
	c.mu.Unlock()
	return result.Text, nil
}

// by — имя способа, которым разобрали речь; пусто — ещё не разбирали.
//
// Значение — из общего словаря каналов (internal/speech: dictation, local,
// gigaam, transcribe, api). Тем же словарём подписана диктовка Harness: история
// у обеих половин одна, и способ в ней обязан читаться одинаково.
func (c *chosen) by() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.channel
}

// typist — руки: набирает текст в запомненное окно.
//
// ЕСЛИ НАБРАТЬ НЕ ВЫШЛО. Молча терять сказанное нельзя: окно могло закрыться,
// человек мог уйти в полноэкранную программу, вставка могла не сработать. Тогда
// первый же отказ запоминается, а весь надиктованный текст уходит в буфер
// обмена — и человеку об этом говорится, а не остаётся гадать, куда делась речь.
type typist struct {
	target uintptr

	mu     sync.Mutex
	reason string
}

func (t *typist) Type(text string) error {
	// Возвращаем фокус перед каждым куском: человек мог щёлкнуть мышью, и тогда
	// текст ушёл бы в чужое окно.
	if t.target == 0 {
		return t.giveUp("не нашлось окна, куда печатать")
	}
	if !typing.Alive(t.target) {
		return t.giveUp("окно, куда печатали, закрылось")
	}
	typing.Focus(t.target)
	if err := typing.Type(text); err != nil {
		return t.giveUp(err.Error())
	}
	return nil
}

// giveUp — запомнить причину и отказаться от набора.
func (t *typist) giveUp(why string) error {
	t.mu.Lock()
	if t.reason == "" {
		t.reason = why
	}
	t.mu.Unlock()
	return errors.New(why)
}

// trouble — почему текст не набрался; пусто — набрался.
func (t *typist) trouble() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reason
}

// tapeAdapter — связывает нарезку речи с диктовкой.
//
// Пакет звука ничего не знает про диктовку, а диктовка — про звуковую
// библиотеку: между ними один маленький переходник, и это дешевле, чем общий
// тип на два пакета.
type tapeAdapter struct{ tape *recorder.Speech }

func (a *tapeAdapter) Start(context.Context) (<-chan dictation.Chunk, error) {
	from, err := a.tape.Start()
	if err != nil {
		return nil, err
	}
	out := make(chan dictation.Chunk, 8)
	go func() {
		defer close(out)
		for one := range from {
			out <- dictation.Chunk{Path: one.Path, Seconds: one.Seconds, Last: one.Last}
		}
	}()
	return out, nil
}

func (a *tapeAdapter) Stop() error    { return a.tape.Stop() }
func (a *tapeAdapter) Level() float64 { return a.tape.Level() }
