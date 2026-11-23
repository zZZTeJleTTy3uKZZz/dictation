// Package recorder — запись встречи в два канала: слева я, справа собеседники.
//
// Порт meetdigest_watcher/recorder.py.
//
// ЗАЧЕМ ДВА КАНАЛА. Распознаватель отдаёт сплошной текст без имён, и
// разделение на голоса он делает догадкой — по тембру, с ошибками на каждом
// перебивании. А ведь на компьютере два звуковых потока УЖЕ разделены
// физически: микрофон — это только я, а то, что играет в динамиках, — только
// остальные. Достаточно не смешивать их в один канал, и вопрос «кто это
// сказал» перестаёт быть догадкой.
//
// ПОЧЕМУ БЕЗ ВИРТУАЛЬНОГО КАБЕЛЯ. Windows умеет отдавать копию того, что
// играет в динамиках (петля WASAPI). Виртуальный кабель делает то же самое, но
// требует установки драйвера и переключения устройства вывода — то есть
// человек однажды не услышит собеседника и не поймёт почему.
//
// ПОЧЕМУ СВЕДЕНИЕ ОТДАНО FFMPEG. Микрофон и динамики идут на разных частотах
// дискретизации. Свой пересчёт здесь означал бы писать собственный ресемплер
// вместо готового и лучшего, ради экономии одного вызова.
package recorder

import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/audioenc"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hidden"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gen2brain/malgo"
)

const (
	// targetRate — распознаватель всё равно понижает звук до 16 кГц.
	targetRate = 16000
	// TwoChannelMark — метка в имени файла.
	//
	// По ней воркер узнаёт свою двухканальную запись и не сводит её в моно
	// перед отправкой. Метка не служебная, а человеческая: видно в папке, что
	// у этой записи голоса разложены по сторонам.
	TwoChannelMark = " (два канала)"
	// mergeTimeout — сколько ждём сведения дорожек.
	mergeTimeout = 10 * time.Minute
)

// ErrUnavailable — звук записать нечем: нет устройств или петли.
var ErrUnavailable = errors.New("записывать звук нечем")

// track — одна дорожка и файл, в который она ложится.
// TrackBitrate — битрейт дорожки встречи в битах в секунду.
//
// Двадцать килобит выбраны владельцем на слух: час разговора это около девяти
// мегабайт вместо шестисот девяноста одного, и для расшифровки этого хватает с
// запасом. Мастер для слушания сжимается отдельно, своим битрейтом.
const TrackBitrate = 20000

type track struct {
	path     string
	file     *os.File
	channels uint32
	rate     uint32
	written  uint32
	device   *malgo.Device
	// encoder пишет дорожку сжатой по ходу встречи. Пока он есть, WAV на диск
	// не ложится: час разговора это 691 МБ на дорожку, а в сжатом виде — 9 МБ.
	encoder *audioenc.Encoder
}

// toMono сводит стерео в моно.
//
// Кодировщик пишет моно, и стерео, поданное ему как есть, он поймёт как моно —
// запись вышла бы вдвое быстрее и с кашей. Дорожка встречи это один источник
// (микрофон отдельно, звук встречи отдельно), поэтому моно здесь не потеря.
func toMono(stereo []byte) []byte {
	frames := len(stereo) / 4
	out := make([]byte, frames*2)
	for i := 0; i < frames; i++ {
		left := int16(binary.LittleEndian.Uint16(stereo[i*4:]))
		right := int16(binary.LittleEndian.Uint16(stereo[i*4+2:]))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16((int32(left)+int32(right))/2)))
	}
	return out
}

// Recorder — пишет микрофон и звук встречи в два файла, потом сводит их.
type Recorder struct {
	folder  string
	bitrate string
	log     *slog.Logger

	// Screen — писать ли заодно экран, и как.
	Screen       bool
	ScreenWidth  string
	ScreenTarget string

	mu      sync.Mutex
	context *malgo.AllocatedContext
	tracks  []*track
	screen  *ScreenRecorder
	started time.Time
	failure error
}

func New(folder, bitrate string, log *slog.Logger) *Recorder {
	return &Recorder{folder: folder, bitrate: bitrate, log: log,
		ScreenWidth: "1280", ScreenTarget: "all"}
}

// Start — начать запись.
func (r *Recorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.tracks) > 0 {
		return nil // уже пишем
	}
	if err := os.MkdirAll(r.folder, 0o755); err != nil {
		return fmt.Errorf("завести папку записей: %w", err)
	}

	context, err := malgo.InitContext([]malgo.Backend{malgo.BackendWasapi}, malgo.ContextConfig{}, nil)
	if err != nil {
		return fmt.Errorf("%w: звуковая система не поднялась: %v", ErrUnavailable, err)
	}
	r.context = context

	stamp := time.Now().Format("20060102-150405")
	// Имена с точки: пока запись идёт, файл недописан, и обход папок не должен
	// его подобрать и отправить посреди разговора.
	sources := []struct {
		kind malgo.DeviceType
		name string
	}{
		{malgo.Capture, fmt.Sprintf(".meet-%s-mic.wav", stamp)},
		{malgo.Loopback, fmt.Sprintf(".meet-%s-room.wav", stamp)},
	}

	for _, source := range sources {
		one, err := r.openTrack(source.kind, filepath.Join(r.folder, source.name))
		if err != nil {
			// Одна дорожка без второй бесполезна: смысл в их разделении.
			// Убираем начатое, чтобы на диске не осталось огрызков.
			r.closeTracks()
			return err
		}
		r.tracks = append(r.tracks, one)
	}

	r.started = time.Now()
	if r.Screen {
		screen := NewScreen(filepath.Join(r.folder, fmt.Sprintf(".meet-%s-screen.mp4", stamp)),
			r.ScreenWidth, r.ScreenTarget, r.log)
		if screen.Start() {
			r.screen = screen
		}
	}
	return nil
}

// openTrack — поднять одну дорожку: устройство пишет прямо в файл.
func (r *Recorder) openTrack(kind malgo.DeviceType, path string) (*track, error) {
	config := malgo.DefaultDeviceConfig(kind)
	config.Capture.Format = malgo.FormatS16
	// Число каналов и частоту берём как есть у устройства: пересчёт отдан
	// ffmpeg при сведении, а здесь важно ничего не потерять.
	config.Capture.Channels = 2
	config.SampleRate = 48000
	// Буфер поменьше: при перегрузке системы большой буфер теряет куски целыми
	// секундами, а щелчок в записи заметнее, чем пропущенные миллисекунды.
	config.PeriodSizeInMilliseconds = 20

	// Сперва пробуем писать сжатое по ходу встречи. Не вышло — пишем WAV, как
	// прежде: отказ кодировщика не должен стоить человеку встречи.
	one := &track{path: path, channels: 2, rate: 48000}
	stem := path[:len(path)-len(filepath.Ext(path))]
	// Кодек встречи — MP3, и это не вкусовщина. Измерено на развёрнутом
	// помощнике: AAC он пишет в MP4, у которого оглавление ложится в конец, и
	// до закрытия файла на диске НЕТ вовсе — отдавать по ходу встречи нечего.
	// MP3 пишется кадрами: файл растёт с первой секунды, а недописанная копия
	// разворачивается в звук без ошибок. Диктовка остаётся на AAC: ей файл
	// нужен целиком, и ломать её незачем.
	encoder, encoderErr := audioenc.StartWithCodec(audioenc.Helper(), filepath.Dir(path),
		filepath.Base(stem), int(one.rate), TrackBitrate, audioenc.CodecMP3)
	if encoderErr != nil {
		// Отказ кодировщика запись не роняет — но и молчать о нём нельзя.
		// Встреча запишется несжатой: 691 МБ на час вместо 9. Без этой строки
		// человек узнал бы о поломке только по размеру файлов, а искал бы
		// причину в чём угодно, кроме кодировщика. Тот же урок, что с отказом
		// распознавания: у отказа должна быть названная причина.
		r.log.Warn("кодировщик не поднялся, пишу WAV",
			"track", filepath.Base(path), "err", encoderErr)
	} else {
		one.encoder = encoder
		// Путь дорожки — тот файл, который помощник пишет сейчас. Нужен затем,
		// чтобы отдать запись по ходу встречи: к моменту остановки он уже
		// закрыт, а во время разговора по нему видно, что успело записаться.
		one.path = encoder.Path()
	}
	var file *os.File
	var err error
	if one.encoder == nil {
		file, err = os.Create(path)
		if err != nil {
			return nil, fmt.Errorf("создать дорожку: %w", err)
		}
		if err := writeWavHeader(file, one.channels, one.rate); err != nil {
			_ = file.Close()
			return nil, err
		}
		one.file = file
	}

	device, err := malgo.InitDevice(r.context.Context, config, malgo.DeviceCallbacks{
		Data: func(_, input []byte, frames uint32) {
			if len(input) == 0 {
				return
			}
			// Ошибку записи не роняем в панику: час разговора дороже
			// стройности, а место на диске кончается предсказуемо.
			if one.encoder != nil {
				if err := one.encoder.Write(toMono(input)); err != nil {
					r.noteFailure(err)
				}
			} else if one.file != nil {
				if _, err := one.file.Write(input); err != nil {
					r.noteFailure(err)
					return
				}
			}
			one.written += frames
		},
	})
	if err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("%w: устройство не открылось (%s): %v", ErrUnavailable, filepath.Base(path), err)
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("%w: запись не началась: %v", ErrUnavailable, err)
	}
	one.device = device
	return one, nil
}

// closeTracks — снять начатые дорожки и убрать их файлы.
//
// Зовётся под уже взятым замком: из Start, когда вторая дорожка не открылась.
func (r *Recorder) closeTracks() {
	for _, one := range r.tracks {
		if one.device != nil {
			one.device.Uninit()
		}
		if one.encoder != nil {
			one.encoder.Abort()
		}
		if one.file != nil {
			_ = one.file.Close()
		}
		_ = os.Remove(one.path)
	}
	r.tracks = nil
	if r.context != nil {
		_ = r.context.Uninit()
		r.context.Free()
		r.context = nil
	}
}

func (r *Recorder) noteFailure(err error) {
	r.mu.Lock()
	if r.failure == nil {
		r.failure = err
	}
	r.mu.Unlock()
}

// Stop — остановить запись и свести дорожки. Возвращает готовый файл.
func (r *Recorder) Stop(title string) (string, error) {
	r.mu.Lock()
	if len(r.tracks) == 0 {
		r.mu.Unlock()
		return "", nil
	}
	tracks := r.tracks
	r.tracks = nil
	screen := r.screen
	r.screen = nil
	failure := r.failure
	r.failure = nil
	r.mu.Unlock()

	for _, one := range tracks {
		if one.device != nil {
			one.device.Uninit()
		}
		if one.encoder != nil {
			// Сжатая дорожка готова: закрываем кодировщик и запоминаем, куда он
			// её положил. Имя другое — расширение дописывает помощник.
			path, err := one.encoder.Finish()
			if err != nil {
				r.log.Warn("дорожка не дописана", "path", filepath.Base(one.path), "err", err)
				continue
			}
			one.path = path
			continue
		}
		if one.file == nil {
			continue
		}
		// Размеры в заголовке WAV становятся известны только сейчас.
		if err := finishWav(one.file, one.channels, one.rate); err != nil {
			r.log.Warn("дорожка не дописана", "path", filepath.Base(one.path), "err", err)
		}
		_ = one.file.Close()
	}
	if r.context != nil {
		_ = r.context.Uninit()
		r.context.Free()
		r.context = nil
	}
	if failure != nil {
		r.log.Warn("запись шла с ошибкой", "err", failure)
	}

	if screen != nil {
		if saved := screen.Stop(); saved != "" {
			target := filepath.Join(r.folder, title+" (экран).mp4")
			if err := os.Rename(saved, target); err != nil {
				r.log.Warn("запись экрана не переименована", "err", err)
			} else {
				r.log.Info("экран записан", "file", filepath.Base(target))
			}
		}
	}

	return r.merge(tracks, title)
}

// Recording — идёт ли запись и сколько уже.
func (r *Recorder) Recording() (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.tracks) == 0 {
		return false, 0
	}
	return true, time.Since(r.started)
}

// mergeFilter — фильтр сведения двух дорожек в одну: микрофон слева, встреча
// справа.
//
// Дорожки приводятся к моно ДО соединения, и это обязательно. Запись встречи
// идёт сжатой и МОНО (кодировщик пишет моно), а запасной путь по-прежнему даёт
// стерео WAV.
//
// Прежний фильтр 0.5*c0+0.5*c1 писался под стерео: на моно-входе второго канала
// нет, ffmpeg берёт его за ноль — и дорожка встречи гасла вдвое. Это нашлось
// измерением уровней по каналам, а не на слух:
//
//	было:  микрофон -22,3 дБ, встреча -28,5 дБ
//	стало: микрофон -22,5 дБ, встреча -22,6 дБ
//
// Тихая половина разговора — это хуже распознавание и заметный перекос
// слушателю, а снаружи выглядело бы как «микрофон пишет громче».
func mergeFilter(rate uint32) string {
	return fmt.Sprintf(
		"[0:a]aresample=%d,aformat=channel_layouts=mono[l];"+
			"[1:a]aresample=%d,aformat=channel_layouts=mono[r];"+
			"[l][r]join=inputs=2:channel_layout=stereo[out]",
		rate, rate)
}

// merge — свести дорожки: левый канал мой микрофон, правый — звук встречи.
func (r *Recorder) merge(tracks []*track, title string) (string, error) {
	if len(tracks) != 2 {
		return r.salvage(tracks)
	}
	target := filepath.Join(r.folder, title+TwoChannelMark+".ogg")
	filter := mergeFilter(targetRate)

	command := hidden.Command("ffmpeg", "-y", "-loglevel", "error",
		"-i", tracks[0].path, "-i", tracks[1].path,
		"-filter_complex", filter,
		"-map", "[out]", "-c:a", "libopus", "-b:a", r.bitrate,
		target)
	command.WaitDelay = mergeTimeout

	if out, err := command.CombinedOutput(); err != nil {
		r.log.Warn("свести дорожки не удалось", "err", err, "вывод", string(out))
		return r.salvage(tracks)
	}
	for _, one := range tracks {
		_ = os.Remove(one.path)
	}
	r.log.Info("встреча записана", "file", filepath.Base(target))
	return target, nil
}

// salvage — сведение не удалось: отдаём хотя бы микрофон.
//
// Час разговора дороже стройности: пусть без разделения на голоса, но
// сказанное сохранится и дойдёт до разбора.
func (r *Recorder) salvage(tracks []*track) (string, error) {
	if len(tracks) == 0 {
		return "", nil
	}
	mic := tracks[0].path
	info, err := os.Stat(mic)
	if err != nil || info.Size() < 1024 {
		return "", fmt.Errorf("записи не получилось")
	}
	// Убираем точку из имени: файл готов, и обход должен его подобрать.
	rescued := filepath.Join(filepath.Dir(mic),
		trimDot(filepath.Base(mic)))
	if err := os.Rename(mic, rescued); err != nil {
		return "", fmt.Errorf("спасти дорожку: %w", err)
	}
	r.log.Warn("дорожки не сведены, отдаю один микрофон", "file", filepath.Base(rescued))
	return rescued, nil
}

func trimDot(name string) string {
	if len(name) > 0 && name[0] == '.' {
		return name[1:]
	}
	return name
}

// ── WAV ───────────────────────────────────────────────────────────────────

// writeWavHeader — заголовок с нулевыми размерами: настоящие станут известны
// в конце записи, и дописывать их надо туда же.
func writeWavHeader(file *os.File, channels, rate uint32) error {
	header := make([]byte, 44)
	copy(header[0:], "RIFF")
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16) // размер блока fmt
	binary.LittleEndian.PutUint16(header[20:], 1)  // PCM
	binary.LittleEndian.PutUint16(header[22:], uint16(channels))
	binary.LittleEndian.PutUint32(header[24:], rate)
	binary.LittleEndian.PutUint32(header[28:], rate*channels*2) // байт в секунду
	binary.LittleEndian.PutUint16(header[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(header[34:], 16) // бит на отсчёт
	copy(header[36:], "data")
	_, err := file.Write(header)
	if err != nil {
		return fmt.Errorf("записать заголовок: %w", err)
	}
	return nil
}

// finishWav — дописать размеры в заголовок.
//
// Без этого файл не откроет ни ffmpeg, ни плеер: в заголовке останутся нули, и
// запись будет выглядеть пустой при полном диске данных.
func finishWav(file *os.File, channels, rate uint32) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("узнать размер дорожки: %w", err)
	}
	total := info.Size()
	if total < 44 {
		return fmt.Errorf("дорожка пуста")
	}
	data := uint32(total - 44)

	if _, err := file.Seek(4, 0); err != nil {
		return err
	}
	if err := binary.Write(file, binary.LittleEndian, uint32(total-8)); err != nil {
		return err
	}
	if _, err := file.Seek(40, 0); err != nil {
		return err
	}
	if err := binary.Write(file, binary.LittleEndian, data); err != nil {
		return err
	}
	_ = channels
	_ = rate
	return file.Sync()
}
