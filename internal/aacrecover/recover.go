package aacrecover

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MinFrameBytes — самый короткий кадр, который имеет смысл пробовать.
	// Кадр тишины у AAC бывает в четыре байта, поэтому нижняя граница низкая.
	MinFrameBytes = 1
	// MaxFrameBytes — предел длины кадра AAC-LC: 6144 бита по стандарту.
	MaxFrameBytes = 2048
	// chunkPayload — сколько сырых байт отдаётся декодеру за один заход.
	// Длина кадра ADTS — тринадцать бит, то есть не больше 8191 байта вместе с
	// семибайтным заголовком; берём предел и оставляем место заголовку.
	chunkPayload = 8191 - adtsHeaderBytes
	// alignBytes — сколько байт после начала mdat можно перебрать, отыскивая
	// первую границу кадра: Media Foundation кладёт перед звуком восемь байт
	// служебного заголовка.
	alignBytes = 64
)

var (
	// ErrAlreadyRecovered — рядом уже лежит восстановленный звук.
	ErrAlreadyRecovered = errors.New("звук уже восстановлен")
	// ErrNoMultiBlock — декодер отказался читать несколько кадров из одного
	// пакета. Такой путь восстановления рассчитан именно на это, и тихо
	// выдавать огрызок вместо звука нельзя.
	ErrNoMultiBlock = errors.New("декодер не читает несколько кадров из одного пакета")
)

// Result — что удалось достать из оборванной записи.
type Result struct {
	// Frames — сколько кадров AAC опознано.
	Frames int
	// Samples — сколько отсчётов звука в них (по 1024 на кадр).
	Samples int
	// Seconds — длительность восстановленного звука.
	Seconds float64
	// Skipped — сколько байт пришлось пропустить, потеряв границу кадра.
	Skipped int
}

// Recover восстанавливает звук и отдаёт готовый WAV в памяти.
func Recover(data []byte, decoder Decoder) ([]byte, Result, error) {
	var pcm bytes.Buffer
	result, err := RecoverTo(&pcm, data, decoder)
	if err != nil {
		return nil, result, err
	}
	return waveFile(pcm.Bytes()), result, nil
}

// RecoverTo ищет звук в оборванном m4a и пишет его PCM в out.
//
// ПОЧЕМУ НЕ ПО КАДРУ. В mdat нет границ кадров: ни таблицы размеров, ни
// синхрослова. Найти границу можно только опытом — декодировать и посмотреть,
// сколько кадров получилось. Перебор длины кадра на каждый кадр записи стоил бы
// двух запусков декодера на кадр: на трёхминутной записи это тысячи запусков и
// десятки минут. Декодер умеет читать **несколько кадров из одного пакета**
// подряд, а больше 8191 байта в один пакет ADTS не влезает. Поэтому за один
// заход декодируется кусок до 8 КБ — это три-четыре десятка кадров, — а где
// именно кончился последний целый кадр, находится двоичным поиском по длине
// (около десятка запусков на кусок). Выходит примерно один запуск на три кадра
// вместо двух на каждый.
func RecoverTo(out io.Writer, data []byte, decoder Decoder) (Result, error) {
	var result Result
	if decoder == nil {
		return result, ErrNoDecoder
	}
	if HasIndex(data) {
		return result, ErrHasIndex
	}
	start, err := SampleOffset(data)
	if err != nil {
		return result, err
	}
	payload := data[start:]
	if len(payload) < MinFrameBytes {
		return result, ErrNoFrames
	}
	pos, ok, err := align(payload, 0, decoder)
	if err != nil {
		return result, err
	}
	if !ok {
		return result, ErrNoFrames
	}
	for pos < len(payload) {
		length := chunkPayload
		if len(payload)-pos < length {
			length = len(payload) - pos
		}
		pcm, frames, err := decoder.Decode(payload, pos, length)
		if err != nil {
			return result, err
		}
		if frames == 0 {
			// Граница потеряна: кусок не разобрался вовсе. Ищем её заново,
			// пропуская байты.
			next, found, err := align(payload, pos+1, decoder)
			if err != nil {
				return result, err
			}
			if !found {
				// Остаток файла — недописанный хвост последнего кадра.
				break
			}
			result.Skipped += next - pos
			pos = next
			continue
		}
		if length == chunkPayload && frames == 1 {
			// Целый кусок в 8 КБ не может содержать один кадр: кадр AAC не
			// длиннее 2048 байт. Значит, декодер прочёл один блок и бросил
			// остальные — на таком декодере этот путь не работает.
			return result, ErrNoMultiBlock
		}
		if _, err := out.Write(pcm); err != nil {
			return result, err
		}
		result.Frames += frames
		result.Samples += frames * FrameSamples
		end, found, err := chunkEnd(payload, pos, length, frames, decoder)
		if err != nil {
			return result, err
		}
		if !found || end <= 0 {
			break
		}
		// chunkEnd возвращает длину куска, а не позицию в файле.
		pos += end
	}
	result.Seconds = float64(result.Samples) / SampleRate
	if result.Frames == 0 {
		return result, ErrNoFrames
	}
	return result, nil
}

// align ищет первое смещение от from, с которого декодируется хоть один кадр.
//
// Смещение нужно потому, что Media Foundation пишет в начало mdat восемь байт
// служебного заголовка: звук начинается не с первого байта содержимого.
func align(data []byte, from int, decoder Decoder) (int, bool, error) {
	for pos := from; pos < len(data) && pos-from <= alignBytes; pos++ {
		length := chunkPayload
		if len(data)-pos < length {
			length = len(data) - pos
		}
		_, frames, err := decoder.Decode(data, pos, length)
		if err != nil {
			return 0, false, err
		}
		if frames > 0 {
			return pos, true, nil
		}
	}
	return 0, false, nil
}

// chunkEnd находит, где кончается последний целый кадр куска.
//
// Признак монотонен: чем длиннее кусок, тем не меньше в нём целых кадров.
// Поэтому граница ищется двоичным поиском. Искать её дальше чем на 2048 байт
// от конца куска не нужно: хвост короче одного кадра, а кадр не длиннее 2048.
func chunkEnd(data []byte, pos, length, frames int, decoder Decoder) (int, bool, error) {
	low := length - (MaxFrameBytes - 1)
	if low < MinFrameBytes {
		low = MinFrameBytes
	}
	high := length
	count, err := decoder.Frames(data, pos, low)
	if err != nil {
		return 0, false, err
	}
	if count >= frames {
		// Последний целый кадр кончился заметно раньше куска: кусок оборвался
		// на ошибке (мусор, чужие данные). Граница может быть где угодно,
		// поэтому сужать поиск до конца куска нельзя.
		low = MinFrameBytes
	}
	for low < high {
		middle := (low + high) / 2
		count, err := decoder.Frames(data, pos, middle)
		if err != nil {
			return 0, false, err
		}
		if count >= frames {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return low, true, nil
}

// RecoverFile восстанавливает звук из файла на диске и кладёт WAV рядом,
// с тем же именем, через временный файл.
//
// ПОЧЕМУ WAV, А НЕ ADTS. Заворачивать каждый найденный кадр в ADTS пришлось бы
// тогда, когда кадры находятся по одному. Здесь же звук уже декодирован
// кусками, и WAV отдаёт его без повторного сжатия — его читает и распознавание,
// и плеер, и он совпадает с тем, что пишут новые записи.
//
// Временный файл нужен, чтобы недописанное восстановление не выглядело готовым
// звуком: при обрыве на диске остаётся .wav.part, и следующий запуск начнёт
// заново, а не примет огрызок за результат.
func RecoverFile(path string, decoder Decoder) (Result, string, error) {
	var result Result
	data, err := os.ReadFile(path)
	if err != nil {
		return result, "", err
	}
	if HasIndex(data) {
		return result, "", ErrHasIndex
	}
	target := strings.TrimSuffix(path, filepath.Ext(path)) + ".wav"
	if info, err := os.Stat(target); err == nil && info.Size() > 44 {
		// Восстановление уже было: второй раз это минуты работы декодера.
		return result, target, ErrAlreadyRecovered
	}
	temporary := target + ".part"
	file, err := os.Create(temporary)
	if err != nil {
		return result, "", err
	}
	if _, err = file.Write(waveHeader(0)); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return result, "", err
	}
	result, err = RecoverTo(file, data, decoder)
	if err == nil {
		_, err = file.WriteAt(waveHeader(uint32(result.Samples*bytesPerSample)), 0)
	}
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporary)
		return result, "", err
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return result, "", err
	}
	return result, target, nil
}

// waveFile — WAV с готовым звуком в памяти.
func waveFile(pcm []byte) []byte {
	header := waveHeader(uint32(len(pcm)))
	return append(header, pcm...)
}

// waveHeader — заголовок WAV: 16 кГц, моно, 16 бит.
func waveHeader(size uint32) []byte {
	header := make([]byte, 44)
	copy(header, "RIFF")
	binary.LittleEndian.PutUint32(header[4:], 36+size)
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], SampleRate)
	binary.LittleEndian.PutUint32(header[28:], SampleRate*bytesPerSample)
	binary.LittleEndian.PutUint16(header[32:], bytesPerSample)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], size)
	return header
}
