// Package dictation — диктовка: текст появляется в поле, пока человек говорит.
//
// ЧЕМ ЭТО ОТЛИЧАЕТСЯ ОТ ЗАМЕТКИ. Заметка — это «сказал и забыл, потом найду».
// Диктовка — «говорю вместо того, чтобы печатать»: текст обязан оказаться в том
// поле, где стоит курсор, и появляться по ходу речи, а не через минуту после.
//
// КАК УСТРОЕНО. Микрофон пишется непрерывно, а звук режется на куски по паузам
// в речи. Кусок закончился — он уходит распознавателю, текст возвращается и
// набирается в активное окно. Пока распознаётся один кусок, человек уже говорит
// следующий: ожидание прячется за речью, и в этом весь фокус.
//
// ПОЧЕМУ РЕЖЕМ ПО ПАУЗАМ, А НЕ ПО ЧАСАМ. Разрезав фразу посередине, получаешь
// два огрызка, которые распознаватель разберёт хуже целого предложения, — и
// склеенный текст будет с ошибками ровно на швах. Пауза в речи — естественный
// шов, на нём резать безопасно.
package dictation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// minChunk — короче этого кусок не отправляем: вдох или щелчок мыши.
//
// Где резать речь, решает пакет звука: там слышно громкость. Здесь остаётся
// только отсеять совсем короткие обрывки, чтобы не платить за пустой запрос.
const minChunk = 400 * time.Millisecond

// Stage — что сейчас происходит. Это же видит человек на плашке.
type Stage string

const (
	StageListening Stage = "слушаю"
	StageThinking  Stage = "распознаю"
	StageTyping    Stage = "печатаю"
	StageDone      Stage = "готово"
	StageTrouble   Stage = "беда"
)

// Event — новость для плашки.
type Event struct {
	Stage Stage `json:"stage"`
	// Text — что распозналось в этом куске (для плашки и для истории).
	Text string `json:"text,omitempty"`
	// Whole — весь текст диктовки целиком.
	Whole string `json:"whole,omitempty"`
	// Level — громкость сейчас, 0..1: плашка рисует по ней полоски, и человек
	// видит, что его слышно, ещё до первого слова.
	Level float64 `json:"level"`
	// Seconds — сколько уже говорим.
	Seconds float64 `json:"seconds"`
	// Trouble — почему не вышло.
	Trouble string `json:"trouble,omitempty"`
}

// Recognizer — чем распознаём кусок.
type Recognizer interface {
	Text(ctx context.Context, path string) (string, error)
}

// Typist — чем набираем текст в чужое окно.
type Typist interface {
	Type(text string) error
}

// Tape — чем пишем звук кусками.
//
// Интерфейс маленький нарочно: в проверках вместо микрофона встаёт пустышка,
// которая отдаёт заранее заготовленные куски.
type Tape interface {
	// Start — открыть микрофон. Отдаёт поток кусков: каждый кусок — путь к файлу
	// с речью между паузами.
	Start(ctx context.Context) (<-chan Chunk, error)
	// Stop — закончить и дописать последний кусок.
	Stop() error
	// Level — текущая громкость, 0..1.
	Level() float64
}

// Chunk — кусок речи.
type Chunk struct {
	Path    string
	Seconds float64
	// Last — последний кусок: после него диктовка закончена.
	Last bool
}

// Dictation — одна диктовка от нажатия до нажатия.
type Dictation struct {
	tape  Tape
	voice Recognizer
	hands Typist
	log   *slog.Logger

	mu      sync.Mutex
	whole   []string
	dropped bool
}

func New(tape Tape, voice Recognizer, hands Typist, log *slog.Logger) *Dictation {
	return &Dictation{tape: tape, voice: voice, hands: hands, log: log}
}

// Run — вести диктовку, пока не остановят.
//
// События уходят в канал: их читает плашка. Канал закрывается в конце — по нему
// плашка и понимает, что всё.
func (d *Dictation) Run(ctx context.Context) (<-chan Event, error) {
	chunks, err := d.tape.Start(ctx)
	if err != nil {
		return nil, err
	}

	events := make(chan Event, 32)
	go func() {
		defer close(events)
		started := time.Now()

		// Пока никто не заговорил, плашка всё равно должна жить: человек видит
		// по полоскам, что его слышат, и не начинает заново.
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				send(events, Event{
					Stage:   StageListening,
					Level:   d.tape.Level(),
					Seconds: time.Since(started).Seconds(),
					Whole:   d.Whole(),
				})
			case chunk, alive := <-chunks:
				if !alive {
					return
				}
				d.handle(ctx, events, chunk, started)
				if chunk.Last {
					send(events, Event{
						Stage:   StageDone,
						Whole:   d.Whole(),
						Seconds: time.Since(started).Seconds(),
					})
					return
				}
			}
		}
	}()
	return events, nil
}

// handle — распознать кусок и набрать его.
func (d *Dictation) handle(ctx context.Context, events chan<- Event, chunk Chunk, started time.Time) {
	if chunk.Path == "" || chunk.Seconds < minChunk.Seconds() {
		return // вдох или щелчок мыши, а не речь
	}
	if d.isDropped() {
		return // человек отменил: печатать больше нечего
	}
	send(events, Event{Stage: StageThinking, Seconds: time.Since(started).Seconds(), Whole: d.Whole()})

	text, err := d.voice.Text(ctx, chunk.Path)
	if err != nil {
		// Кусок не разобрался — диктовка продолжается. Остановить её из-за
		// одного куска значило бы потерять и всё остальное.
		d.log.Warn("кусок не распознан", "err", err)
		send(events, Event{Stage: StageTrouble, Trouble: err.Error(), Whole: d.Whole()})
		return
	}
	if text == "" {
		return
	}

	// Пробел между кусками ставим здесь: распознаватель отдаёт каждый кусок как
	// отдельное предложение, и без пробела слова слипаются на швах.
	if d.Whole() != "" {
		text = " " + text
	}

	send(events, Event{
		Stage: StageTyping, Text: text, Whole: d.Whole() + text,
		Seconds: time.Since(started).Seconds(),
	})
	if err := d.hands.Type(text); err != nil {
		d.log.Error("текст не набрался", "err", err)
		send(events, Event{Stage: StageTrouble, Trouble: err.Error(), Whole: d.Whole()})
		return
	}

	d.mu.Lock()
	d.whole = append(d.whole, text)
	d.mu.Unlock()
}

// Stop — закончить диктовку.
func (d *Dictation) Stop() error { return d.tape.Stop() }

// Drop — бросить: остановиться и больше ничего не печатать.
//
// Отмена приходит, когда человек передумал уже во время речи. Напечатанное
// вернуть нельзя, но доделывать начатое после отмены — значит печатать в поле
// то, от чего он только что отказался.
func (d *Dictation) Drop() {
	d.mu.Lock()
	d.dropped = true
	d.mu.Unlock()
}

// dropped — отменили ли диктовку.
func (d *Dictation) isDropped() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped
}

// Whole — весь надиктованный текст.
func (d *Dictation) Whole() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	whole := ""
	for _, part := range d.whole {
		whole += part
	}
	return whole
}

// send — положить событие, не заблокировавшись.
//
// Плашка может не успевать (она перерисовывается), и ждать её нельзя: диктовка
// идёт в реальном времени, а пропущенная полоска громкости никому не нужна.
func send(events chan<- Event, one Event) {
	select {
	case events <- one:
	default:
	}
}

// Describe — что показать человеку на плашке одной строкой.
func Describe(one Event) string {
	switch one.Stage {
	case StageListening:
		return fmt.Sprintf("Слушаю · %.0f с", one.Seconds)
	case StageThinking:
		return "Распознаю…"
	case StageTyping:
		return "Печатаю"
	case StageDone:
		return "Готово"
	case StageTrouble:
		return "Не вышло: " + one.Trouble
	}
	return string(one.Stage)
}
