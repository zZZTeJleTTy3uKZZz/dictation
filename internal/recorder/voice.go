// Запись голоса: только микрофон, для заметок.
//
// ЧЕМ ОТЛИЧАЕТСЯ ОТ ЗАПИСИ ВСТРЕЧИ. У встречи два источника (я и собеседники) и
// два канала, чтобы потом было понятно, кто говорил. У заметки источник один —
// человек диктует сам себе, — и второй канал был бы пустой дорожкой, которая
// только удваивает файл и путает распознаватель.
//
// Моно, 16 кГц: ровно то, что распознаватель и так делает со звуком сам. Писать
// подробнее — значит потратить диск и время на отправку, не добавив ни слова к
// расшифровке.
package recorder

import (
	"fmt"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hidden"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/gen2brain/malgo"
)

// Voice — запись голоса с микрофона.
type Voice struct {
	folder string
	log    *slog.Logger

	mu      sync.Mutex
	context *malgo.AllocatedContext
	tape    *track
	started time.Time
}

func NewVoice(folder string, log *slog.Logger) *Voice {
	return &Voice{folder: folder, log: log}
}

// Start — начать запись.
func (v *Voice) Start() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.tape != nil {
		return fmt.Errorf("запись уже идёт")
	}
	if err := os.MkdirAll(v.folder, 0o755); err != nil {
		return fmt.Errorf("завести папку заметок: %w", err)
	}

	made, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return fmt.Errorf("звук недоступен: %w", err)
	}
	v.context = made

	path := filepath.Join(v.folder, fmt.Sprintf("заметка-%s.wav", time.Now().Format("20060102-150405")))
	tape, err := v.openTape(path)
	if err != nil {
		_ = made.Uninit()
		made.Free()
		v.context = nil
		return err
	}
	v.tape = tape
	v.started = time.Now()
	return nil
}

// openTape — открыть микрофон и файл под него.
func (v *Voice) openTape(path string) (*track, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("создать файл заметки: %w", err)
	}
	if err := writeWavHeader(file, 1, targetRate); err != nil {
		_ = file.Close()
		return nil, err
	}

	settings := malgo.DefaultDeviceConfig(malgo.Capture)
	settings.Capture.Format = malgo.FormatS16
	settings.Capture.Channels = 1
	settings.SampleRate = targetRate

	tape := &track{path: path, file: file, channels: 1, rate: targetRate}
	device, err := malgo.InitDevice(v.context.Context, settings, malgo.DeviceCallbacks{
		Data: func(_, sample []byte, count uint32) {
			if _, err := tape.file.Write(sample); err == nil {
				tape.written += count
			}
		},
	})
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("микрофон не открылся: %w", err)
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		_ = file.Close()
		return nil, fmt.Errorf("микрофон не начал писать: %w", err)
	}
	tape.device = device
	return tape, nil
}

// Recording — идёт ли запись и сколько уже.
func (v *Voice) Recording() (bool, time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.tape == nil {
		return false, 0
	}
	return true, time.Since(v.started)
}

// Stop — закончить запись и отдать путь к готовому файлу.
//
// Возвращает длительность: она нужна истории, а считать её по размеру файла
// пришлось бы заново и с оглядкой на формат.
func (v *Voice) Stop() (string, time.Duration, error) {
	v.mu.Lock()
	tape, context := v.tape, v.context
	started := v.started
	v.tape, v.context = nil, nil
	v.mu.Unlock()

	if tape == nil {
		return "", 0, fmt.Errorf("запись не шла")
	}
	passed := time.Since(started)

	if tape.device != nil {
		_ = tape.device.Stop()
		tape.device.Uninit()
	}
	if context != nil {
		_ = context.Uninit()
		context.Free()
	}

	// Размеры в заголовок дописываются здесь: без этого файл не откроет ни
	// плеер, ни распознаватель — при полном диске данных.
	if err := finishWav(tape.file, tape.channels, tape.rate); err != nil {
		_ = tape.file.Close()
		return "", passed, err
	}
	if err := tape.file.Close(); err != nil {
		return "", passed, fmt.Errorf("закрыть файл заметки: %w", err)
	}

	if tape.written == 0 {
		// Ни одного отсчёта: микрофон занят другой программой или выключен.
		// Пустой файл не отдаём — он превратился бы в пустую заметку.
		_ = os.Remove(tape.path)
		return "", passed, fmt.Errorf("с микрофона не пришло ни звука")
	}

	// Сжимаем, если есть чем: для заметки это разница между мегабайтами и
	// сотнями килобайт. Нет ffmpeg — отдаём WAV, он тоже годится.
	if packed, err := v.pack(tape.path); err == nil {
		return packed, passed, nil
	}
	return tape.path, passed, nil
}

// pack — ужать WAV в opus.
func (v *Voice) pack(path string) (string, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", err
	}
	packed := path[:len(path)-len(filepath.Ext(path))] + ".opus"

	command := hidden.Command("ffmpeg", "-y", "-loglevel", "error",
		"-i", path, "-c:a", "libopus", "-b:a", "24k", "-ac", "1", packed)
	if err := command.Run(); err != nil {
		return "", err
	}
	info, err := os.Stat(packed)
	if err != nil || info.Size() == 0 {
		return "", fmt.Errorf("сжатая заметка пуста")
	}
	_ = os.Remove(path)
	return packed, nil
}
