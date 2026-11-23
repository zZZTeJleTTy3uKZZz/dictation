// Package dictaphone — заметка голосом по нажатию сочетания клавиш.
//
// КАК ЭТО РАБОТАЕТ ДЛЯ ЧЕЛОВЕКА. Нажал — пишется. Нажал снова — запись
// остановилась, через несколько секунд в истории появился текст. Всё.
//
// ПОЧЕМУ ОДНА И ТА ЖЕ КЛАВИША, А НЕ «ДЕРЖАТЬ». Держать клавишу, пока говоришь,
// можно только короткую фразу; мысль длиной в полминуты так не запишешь, а руки
// заняты. Переключение работает и для трёх секунд, и для трёх минут.
//
// ПОЧЕМУ РАСШИФРОВКА ОТДЕЛЬНО ОТ ЗАПИСИ. Заметка попадает в историю сразу после
// остановки — ещё без текста. Распознавание может не получиться (нет сети,
// кончился ключ, не установлена модель), и тогда останется звук, который человек
// послушает сам. Потерять мысль из-за отказа чужого сервиса недопустимо.
package dictaphone

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/notes"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
)

// tooShort — короче этого заметку не сохраняем.
//
// Полсекунды: столько длится случайное нажатие. Сохранять его — значит копить в
// истории мусор, в котором потом не найти настоящие заметки.
const tooShort = 500 * time.Millisecond

// Tape — чем пишется голос. За интерфейсом стоит recorder.Voice; в проверках —
// пустышка, потому что открывать микрофон посреди проверки нельзя.
type Tape interface {
	Start() error
	Stop() (string, time.Duration, error)
	Recording() (bool, time.Duration)
}

// Dictaphone — заметки голосом.
type Dictaphone struct {
	store  *notes.Store
	set    *speech.Set
	log    *slog.Logger
	device string

	// Open — чем писать. Отдельным полем, чтобы каждая заметка начиналась с
	// чистой записи: микрофон держать открытым между заметками нельзя — Windows
	// покажет человеку, что его слушают, и будет права.
	Open func() Tape
	// Channel — какой способ расшифровки просить; читается на каждую заметку,
	// потому что человек меняет его в настройках, не перезапуская программу.
	Channel func() string
	// Now — текущее время; в проверках его двигают руками.
	Now func() time.Time

	mu      sync.Mutex
	tape    Tape
	started time.Time
	// done — закрывается, когда расшифровка дописана. Нужен проверкам, чтобы не
	// спать в ожидании, и никому больше.
	done chan struct{}
}

func New(store *notes.Store, set *speech.Set, device string, log *slog.Logger) *Dictaphone {
	return &Dictaphone{
		store: store, set: set, device: device, log: log,
		Now:     time.Now,
		Channel: func() string { return "auto" },
	}
}

// Toggle — нажали сочетание: начать или закончить.
//
// Возвращает, идёт ли запись после нажатия: по этому значку в лотке есть что
// показать, не спрашивая ничего дополнительно.
func (d *Dictaphone) Toggle(ctx context.Context) bool {
	if recording, _ := d.Recording(); recording {
		d.Finish(ctx)
		return false
	}
	return d.Begin()
}

// Begin — начать запись.
func (d *Dictaphone) Begin() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.tape != nil {
		return true
	}
	if d.Open == nil {
		d.log.Error("заметку записать нечем: не задана запись")
		return false
	}

	tape := d.Open()
	if err := tape.Start(); err != nil {
		// Чаще всего это «микрофон занят другой программой»: человек должен
		// узнать причину, а не нажимать ещё раз в тишину.
		d.log.Error("заметка не началась", "err", err)
		return false
	}
	d.tape = tape
	d.started = d.Now()
	d.done = make(chan struct{})
	d.log.Info("пишу заметку")
	return true
}

// Finish — закончить запись, сохранить и расшифровать.
func (d *Dictaphone) Finish(ctx context.Context) {
	d.mu.Lock()
	tape, started, done := d.tape, d.started, d.done
	d.tape = nil
	d.mu.Unlock()

	if tape == nil {
		return
	}
	closeDone := func() {
		if done != nil {
			close(done)
		}
	}

	path, passed, err := tape.Stop()
	if err != nil {
		d.log.Error("заметка не сохранилась", "err", err)
		closeDone()
		return
	}
	if passed < tooShort {
		d.log.Info("заметка слишком коротка, выбрасываю", "мс", passed.Milliseconds())
		closeDone()
		return
	}

	note := notes.Note{
		ID:      notes.NewID(started),
		At:      started,
		Seconds: passed.Seconds(),
		Device:  d.device,
		// Заметку голосом пишет значок: это его сочетание клавиш и его история.
		// Поле нужно, чтобы в общей истории было видно, откуда пришла запись.
		Source: notes.SourceWidget,
		Audio:  path,
	}
	if err := d.store.Add(note); err != nil {
		d.log.Error("заметка не попала в историю", "err", err)
		closeDone()
		return
	}
	d.log.Info("заметка записана", "id", note.ID, "секунд", int(note.Seconds))

	// Расшифровка — в своей горутине: нажатие не должно ждать сеть. Остановка
	// по ctx, чтобы при выключении мы не держали процесс.
	go func() {
		defer closeDone()
		d.transcribe(ctx, note.ID, path)
	}()
}

// transcribe — дописать текст к заметке.
func (d *Dictaphone) transcribe(ctx context.Context, id, path string) {
	result, note, err := d.set.Text(ctx, d.Channel(), path)
	if err != nil {
		// Причина пишется в заметку, а не только в журнал: человек увидит её
		// прямо в истории и поймёт, что делать (включить сеть, вписать ключ).
		if saved := d.store.Update(id, func(one *notes.Note) {
			one.Trouble = err.Error()
		}); saved != nil {
			d.log.Error("причина не записалась", "err", saved)
		}
		d.log.Warn("заметка без расшифровки", "id", id, "err", err)
		return
	}
	if note != "" {
		d.log.Info("о выборе распознавателя", "что", note)
	}

	if err := d.store.Update(id, func(one *notes.Note) {
		one.Text = result.Text
		one.Speech = result.By
		one.Trouble = ""
	}); err != nil {
		d.log.Error("расшифровка не записалась", "err", err)
		return
	}
	d.log.Info("заметка расшифрована", "id", id, "чем", result.By)
}

// Recording — идёт ли запись и сколько уже.
func (d *Dictaphone) Recording() (bool, time.Duration) {
	d.mu.Lock()
	tape, started := d.tape, d.started
	d.mu.Unlock()
	if tape == nil {
		return false, 0
	}
	return true, d.Now().Sub(started)
}

// Wait — дождаться, пока последняя заметка будет дописана.
//
// Для выключения (дописать начатое, а не бросить) и для проверок.
func (d *Dictaphone) Wait(ctx context.Context) {
	d.mu.Lock()
	done := d.done
	d.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}
