package localdict

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/dictsegment"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/opusrec"
)

const sampleRate = 16000

type Audio struct {
	mu       sync.Mutex
	context  *malgo.AllocatedContext
	device   *malgo.Device
	samples  []byte
	consumed int
	level    atomic.Uint64
	recorder *opusrec.Recorder
	sinkPath string
}

func (a *Audio) Level() float64 { return math.Float64frombits(a.level.Load()) }

// RecordedPath называет файл, в который идёт запись сейчас. Пустая строка —
// записи нет.
func (a *Audio) RecordedPath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sinkPath
}
func (a *Audio) Start() error {
	if a.device != nil {
		return fmt.Errorf("already recording")
	}
	a.samples = nil
	a.consumed = 0
	a.level.Store(0)
	context, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return err
	}
	settings := malgo.DefaultDeviceConfig(malgo.Capture)
	settings.Capture.Format = malgo.FormatS16
	settings.Capture.Channels = 1
	settings.SampleRate = sampleRate
	device, err := malgo.InitDevice(context.Context, settings, malgo.DeviceCallbacks{Data: func(_, data []byte, _ uint32) {
		var energy float64
		for i := 0; i+1 < len(data); i += 2 {
			value := float64(int16(binary.LittleEndian.Uint16(data[i:]))) / 32768
			energy += value * value
		}
		if len(data) > 0 {
			a.level.Store(math.Float64bits(math.Sqrt(energy / float64(len(data)/2))))
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		// Nothing is dropped and nothing is bounded: an hour-long conversation
		// must be recorded whole. Consumed audio is released below instead of
		// being clipped at an arbitrary length.
		a.samples = append(a.samples, data...)
		a.writeSinkLocked(data)
	}})
	if err != nil {
		_ = context.Uninit()
		context.Free()
		return err
	}
	if err = device.Start(); err != nil {
		device.Uninit()
		_ = context.Uninit()
		context.Free()
		return err
	}
	a.context, a.device = context, device
	return nil
}

// Record пишет речь в Ogg/Opus по ходу разговора.
//
// ПОЧЕМУ СРАЗУ OPUS, А НЕ WAV. Час диктовки в WAV — сто пятнадцать мегабайт, и
// их всё равно пришлось бы сжимать вторым заходом. Сжатие по ходу отдаёт на
// диск около восемнадцати мегабайт в час, а страницы Ogg самодостаточны: вылет
// процесса или выключение питания оставляют файл, который открывает и плеер, и
// распознаватель — до последней страницы, сброшенной на диск.
//
// Прежний путь (AAC в m4a) этого не умел: оглавление MP4 ложится в конец, и до
// закрытия файла читать было нечего. Отсюда и запасной WAV, который теперь не
// нужен.
func (a *Audio) Record(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	recorder, err := opusrec.Start(path)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recorder, a.sinkPath = recorder, path
	return nil
}

func (a *Audio) writeSinkLocked(data []byte) {
	if len(data) == 0 || a.recorder == nil {
		return
	}
	if err := a.recorder.Write(data); err != nil {
		// Ошибка записи — это событие, а не тишина: человек должен по журналу
		// узнать, что звук перестал ложиться на диск, вместо того чтобы искать
		// причину в распознавателе.
		path := a.sinkPath
		a.recorder.Abort()
		a.recorder, a.sinkPath = nil, ""
		if path != "" {
			logLine(filepath.Dir(path), "recording-write-failed",
				"audio="+filepath.Base(path)+" err="+err.Error())
		}
	}
}

// CloseRecording closes the Ogg stream and returns the file that holds the whole
// session audio. Закрытие пишет только пометку о конце: звук уже на диске,
// страницами по секунде.
func (a *Audio) CloseRecording() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.recorder == nil {
		return ""
	}
	path := a.recorder.Close()
	a.recorder, a.sinkPath = nil, ""
	if path == "" {
		return ""
	}
	return path
}

// release drops audio the recognizer has already committed. The window that
// stays in memory is the unfinished tail, so recording length no longer decides
// memory use.
func (a *Audio) release() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.consumed < 1<<16 {
		return
	}
	a.samples = append([]byte(nil), a.samples[a.consumed:]...)
	a.consumed = 0
}

func (a *Audio) Stop() []byte {
	if a.device != nil {
		_ = a.device.Stop()
		a.device.Uninit()
		a.device = nil
	}
	if a.context != nil {
		_ = a.context.Uninit()
		a.context.Free()
		a.context = nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	data := append([]byte(nil), a.samples...)
	return data
}

// NextChunk commits audio once, preferring a speech pause to a hard boundary.
// The recognizer never receives an already committed region again.
func (a *Audio) NextChunk(final bool) []byte {
	return a.nextChunk(final, 12)
}
func (a *Audio) nextChunk(final bool, quietFrames int) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	data := a.samples[a.consumed:]
	end := 0
	if final {
		end = len(data)
	} else {
		quiet := 0
		for i := 0; i+640 <= len(data); i += 640 {
			var energy float64
			for j := i; j < i+640; j += 2 {
				v := float64(int16(binary.LittleEndian.Uint16(data[j:]))) / 32768
				energy += v * v
			}
			if math.Sqrt(energy/320) < .008 {
				quiet++
			} else {
				quiet = 0
			}
			if i >= sampleRate && quiet >= quietFrames {
				end = i + 640
				break
			}
			// Rolling recognition streams stable words in continuous speech;
			// only bound the utterance here to limit decoding cost.
			if i >= sampleRate*2*30 {
				end = i + 640
				break
			}
		}
	}
	if end == 0 {
		return nil
	}
	chunk := append([]byte(nil), data[:end]...)
	a.consumed += end
	return chunk
}

// NextSegment отдаёт готовый кусок накопленного звука, не дожидаясь конца
// записи. Кусок кончается на паузе не короче 600 мс, но не раньше двадцати и не
// позже шестидесяти секунд звука (см. dictsegment.Boundary). Пусто — кусок ещё
// не готов.
//
// Отданный звук больше не вернётся: он уже ушёл в разбор. Поэтому зовётся
// только тогда, когда есть куда положить кусок (очередь разбора не полна), —
// иначе начало речи пропало бы между памятью и облаком.
func (a *Audio) NextSegment() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending := a.samples[a.consumed:]
	end := dictsegment.Boundary(pending)
	if end == 0 {
		return nil
	}
	chunk := append([]byte(nil), pending[:end]...)
	a.consumed += end
	return chunk
}

// All отдаёт весь звук сессии целиком. Нужен запасному пути: когда кусок не
// удалось разобрать, запись уходит в разбор одним запросом, как было до нарезки.
func (a *Audio) All() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]byte(nil), a.samples...)
}

func (a *Audio) PreviewChunk() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]byte(nil), a.samples[a.consumed:]...)
}

// QuietDuration examines captured audio rather than recognition timestamps.
// Decoding and cloud latency must not change the user's pause setting.
func (a *Audio) QuietDuration() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	frames := 0
	for end := len(a.samples); end >= 640 && frames < 250; end -= 640 {
		var energy float64
		for offset := end - 640; offset < end; offset += 2 {
			value := float64(int16(binary.LittleEndian.Uint16(a.samples[offset:]))) / 32768
			energy += value * value
		}
		if math.Sqrt(energy/320) >= .008 {
			break
		}
		frames++
	}
	return time.Duration(frames) * 20 * time.Millisecond
}
func HasSpeech(data []byte) bool {
	voiced := 0
	for i := 0; i+640 <= len(data); i += 640 {
		var energy float64
		for j := i; j < i+640; j += 2 {
			value := float64(int16(binary.LittleEndian.Uint16(data[j:]))) / 32768
			energy += value * value
		}
		if math.Sqrt(energy/320) > .008 {
			voiced++
		}
	}
	return voiced >= 6
}

func waveHeader(size uint32) []byte {
	header := make([]byte, 44)
	copy(header, "RIFF")
	binary.LittleEndian.PutUint32(header[4:], 36+size)
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], sampleRate)
	binary.LittleEndian.PutUint32(header[28:], sampleRate*2)
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], size)
	return header
}

func WriteWave(path string, data []byte) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if _, err = file.Write(waveHeader(uint32(len(data)))); err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

// repairRecordings fixes the size fields of session files whose process was
// killed before the header caught up. The audio is on disk; only the declared
// length may be short.
func repairRecordings(dir string) {
	paths, err := filepath.Glob(filepath.Join(dir, "recordings", "*.wav"))
	if err != nil {
		return
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.Size() <= 44 {
			continue
		}
		file, err := os.OpenFile(path, os.O_RDWR, 0600)
		if err != nil {
			continue
		}
		header := make([]byte, 44)
		if _, err = file.ReadAt(header, 0); err == nil {
			declared := int64(binary.LittleEndian.Uint32(header[40:]))
			if declared+44 != info.Size() {
				_, _ = file.WriteAt(waveHeader(uint32(info.Size()-44)), 0)
				_ = file.Sync()
			}
		}
		_ = file.Close()
	}
}
