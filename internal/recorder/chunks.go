// Нарезка речи на куски по паузам — для диктовки.
//
// ЗАЧЕМ. Чтобы текст появлялся, пока человек говорит, распознавателю надо давать
// материал по ходу дела. Резать по часам нельзя: разрезанная посередине фраза
// разбирается хуже целой, и склейка получается с ошибками ровно на швах. Пауза
// в речи — естественный шов.
//
// КАК СЧИТАЕМ ГРОМКОСТЬ. По среднему квадрату отсчётов (RMS) — это и есть
// «насколько громко» в том смысле, в каком слышит ухо. Порог грубый нарочно:
// задача не измерить, а отличить «говорит» от «молчит».
package recorder

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
)

const (
	// silenceToCut — пауза, после которой кусок закончен.
	//
	// 350 мс: это короткая запятая в речи, а не конец мысли. Было 700 — и текст
	// ждал, пока человек договорит фразу до точки; ощущалось это как «оно
	// тормозит», хотя распознавание занимает полсекунды.
	silenceToCut = 350 * time.Millisecond
	// maxChunk — предел длины куска: человек может говорить без пауз.
	//
	// 3 секунды. Раньше было 12: тот, кто говорит без остановки, ждал свой текст
	// двенадцать секунд. Короткий кусок распознаётся чуть хуже длинного, но
	// текст, который появляется через три секунды, полезнее точного через
	// двенадцать.
	maxChunk = 3 * time.Second
	// quietLevel — порог тишины, доля от предела громкости.
	quietLevel = 0.02
	// keepBefore — сколько звука до начала речи оставлять в куске.
	//
	// Четверть секунды: без неё срезается начало первого слова, и «позвонить»
	// превращается в «звонить».
	keepBefore = 250 * time.Millisecond
)

// Speech — микрофон, режущий речь на куски.
type Speech struct {
	folder string
	log    *slog.Logger

	mu      sync.Mutex
	context *malgo.AllocatedContext
	device  *malgo.Device

	// level — последняя громкость; читается плашкой из другой горутины.
	level atomic.Uint64

	chunks  chan Chunk
	current *os.File
	path    string
	written uint32
	// quietFor — сколько отсчётов подряд было тихо.
	quietFor uint32
	// before — хвост тишины перед речью: кладём его в начало куска.
	before   []byte
	speaking bool
	stopping atomic.Bool
	count    int
}

// Chunk — кусок речи. Повторяет тип из пакета dictation по значению, чтобы
// пакеты не зависели друг от друга: звук ничего не должен знать о диктовке.
type Chunk struct {
	Path    string
	Seconds float64
	Last    bool
}

func NewSpeech(folder string, log *slog.Logger) *Speech {
	return &Speech{folder: folder, log: log}
}

// Level — текущая громкость, 0..1.
func (s *Speech) Level() float64 {
	return math.Float64frombits(s.level.Load())
}

// Start — открыть микрофон и начать резать.
func (s *Speech) Start() (<-chan Chunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.device != nil {
		return nil, fmt.Errorf("диктовка уже идёт")
	}
	if err := os.MkdirAll(s.folder, 0o755); err != nil {
		return nil, fmt.Errorf("завести папку диктовки: %w", err)
	}

	made, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("звук недоступен: %w", err)
	}

	settings := malgo.DefaultDeviceConfig(malgo.Capture)
	settings.Capture.Format = malgo.FormatS16
	settings.Capture.Channels = 1
	settings.SampleRate = targetRate

	s.chunks = make(chan Chunk, 8)
	s.context = made

	device, err := malgo.InitDevice(made.Context, settings, malgo.DeviceCallbacks{Data: s.heard})
	if err != nil {
		_ = made.Uninit()
		made.Free()
		s.context = nil
		return nil, fmt.Errorf("микрофон не открылся: %w", err)
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		_ = made.Uninit()
		made.Free()
		s.context = nil
		return nil, fmt.Errorf("микрофон не начал писать: %w", err)
	}
	s.device = device
	return s.chunks, nil
}

// heard — пришёл звук с микрофона.
//
// Вызывается звуковой библиотекой в её собственном потоке, часто и помалу:
// здесь нельзя ни ждать, ни брать замки надолго — иначе звук начнёт заикаться.
func (s *Speech) heard(_, sample []byte, _ uint32) {
	loud := loudness(sample)
	s.level.Store(math.Float64bits(loud))

	frames := uint32(len(sample) / 2)
	if loud >= quietLevel {
		s.quietFor = 0
		if !s.speaking {
			s.speaking = true
			s.open()
		}
	} else if s.speaking {
		s.quietFor += frames
	}

	if !s.speaking {
		// Копим хвост тишины: без него срезается начало первого слова.
		s.before = append(s.before, sample...)
		limit := int(keepBefore.Seconds() * targetRate * 2)
		if len(s.before) > limit {
			s.before = s.before[len(s.before)-limit:]
		}
		return
	}

	if s.current != nil {
		if _, err := s.current.Write(sample); err == nil {
			s.written += frames
		}
	}

	tooLong := float64(s.written)/targetRate >= maxChunk.Seconds()
	enoughSilence := float64(s.quietFor)/targetRate >= silenceToCut.Seconds()
	if enoughSilence {
		s.cut(false)
		return
	}
	if tooLong {
		// Режем посреди речи только когда иначе нельзя, и ждём ближайшего
		// затишья — хоть полсекунды: разрез посреди слова распознаватель
		// разбирает заметно хуже, чем разрез на вдохе.
		if loud < quietLevel*2 || float64(s.written)/targetRate >= maxChunk.Seconds()*1.6 {
			s.cut(false)
		}
	}
}

// open — начать новый кусок.
func (s *Speech) open() {
	s.count++
	s.path = filepath.Join(s.folder, fmt.Sprintf("кусок-%d-%d.wav", time.Now().Unix(), s.count))
	file, err := os.Create(s.path)
	if err != nil {
		s.log.Error("кусок не создался", "err", err)
		s.speaking = false
		return
	}
	if err := writeWavHeader(file, 1, targetRate); err != nil {
		_ = file.Close()
		s.speaking = false
		return
	}
	// Хвост тишины перед речью — в начало куска.
	if len(s.before) > 0 {
		if _, err := file.Write(s.before); err == nil {
			s.written = uint32(len(s.before) / 2)
		}
		s.before = s.before[:0]
	}
	s.current = file
}

// cut — закрыть кусок и отдать его распознавателю.
func (s *Speech) cut(last bool) {
	file, path, written := s.current, s.path, s.written
	s.current, s.path, s.written = nil, "", 0
	s.quietFor = 0
	s.speaking = false

	if file == nil {
		if last {
			s.finish(Chunk{Last: true})
		}
		return
	}
	if err := finishWav(file, 1, targetRate); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return
	}
	_ = file.Close()

	seconds := float64(written) / targetRate
	if seconds < 0.4 {
		// Щелчок мыши или вдох: отправлять нечего, а платный запрос впустую.
		_ = os.Remove(path)
		if last {
			s.finish(Chunk{Last: true})
		}
		return
	}
	s.finish(Chunk{Path: path, Seconds: seconds, Last: last})
}

// finish — отдать кусок, не заблокировав звуковой поток.
func (s *Speech) finish(chunk Chunk) {
	select {
	case s.chunks <- chunk:
	default:
		// Очередь забита: распознаватель не успевает. Кусок теряем осознанно —
		// заикающийся микрофон хуже пропущенной фразы, а человек это услышит.
		s.log.Warn("кусок выброшен: распознаватель не успевает")
		if chunk.Path != "" {
			_ = os.Remove(chunk.Path)
		}
	}
}

// Stop — закончить диктовку, дописав последний кусок.
func (s *Speech) Stop() error {
	s.mu.Lock()
	device, context := s.device, s.context
	s.device, s.context = nil, nil
	s.mu.Unlock()

	if device == nil {
		return nil
	}
	s.stopping.Store(true)
	_ = device.Stop()
	device.Uninit()
	if context != nil {
		_ = context.Uninit()
		context.Free()
	}

	// Последний кусок дописываем уже без микрофона: человек договорил, и эти
	// слова — обычно самые важные.
	s.cut(true)
	close(s.chunks)
	return nil
}

// loudness — громкость куска звука, 0..1.
func loudness(sample []byte) float64 {
	if len(sample) < 2 {
		return 0
	}
	var sum float64
	count := len(sample) / 2
	for index := 0; index+1 < len(sample); index += 2 {
		value := float64(int16(uint16(sample[index]) | uint16(sample[index+1])<<8))
		sum += value * value
	}
	return math.Sqrt(sum/float64(count)) / 32768
}
