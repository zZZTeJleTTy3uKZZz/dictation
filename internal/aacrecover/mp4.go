// Package aacrecover достаёт звук из дорожки .m4a, которую оборвала авария.
//
// ОТКУДА ЭТО. Старая диктовка писала сжатый звук помощником audio-compress,
// а тот складывает AAC в MP4. У MP4 оглавление (moov) ложится в КОНЕЦ файла,
// при закрытии. 11.11.2026 Windows упала посреди диктовки, и на диске остался
// mdat без moov: звук в нём есть, а открыть файл нечем — ни ffmpeg, ни плеер.
//
// ЗВУК ТАМ ЛЕЖИТ СЫРЫМИ КАДРАМИ AAC-LC. Их параметры известны заранее: их
// пишет наш же помощник, 16 кГц, моно, AAC-LC. Значит звук можно достать не
// чтением оглавления, а декодированием: кадр — это ровно 1024 отсчёта.
// Границ кадров в mdat нет, поэтому конец куска ищется опытом. Способ проверен
// на настоящем битом файле: 503 КБ → 2622 кадра → 2:48 звука.
//
// ДЕКОДЕР — НЕ СВОЙ. Чистого Go-декодера AAC в зависимостях нет, а писать его
// ради восстановления старых записей незачем: ffmpeg уже нужен программе для
// сведения дорожек встречи, и он умеет ровно то, что надо — читает несколько
// кадров подряд и отдаёт звук. Запасного пути нет умышленно: если ffmpeg на
// машине нет, честнее сказать это в журнал, чем выдать полувосстановленный
// звук (см. ErrNoDecoder).
//
// Новые записи сюда не попадают: диктовка пишет WAV (см. internal/localdict),
// и WAV переживает аварию без всякого восстановления.
package aacrecover

import (
	"encoding/binary"
	"errors"
)

var (
	// ErrNoSampleData — в файле нет бокса mdat: восстанавливать нечего.
	ErrNoSampleData = errors.New("в файле нет звуковых данных (бокс mdat не найден)")
	// ErrHasIndex — в файле уже есть оглавление: он целый.
	ErrHasIndex = errors.New("в файле есть оглавление moov — файл целый")
	// ErrNoFrames — байты есть, но ни одного кадра AAC в них не опознано.
	ErrNoFrames = errors.New("кадры AAC не найдены")
	// ErrNoDecoder — восстанавливать нечем: на машине нет ffmpeg.
	ErrNoDecoder = errors.New("нет ffmpeg — восстанавливать звук нечем")
)

// box — разметка верхнего уровня файла MP4: размер и имя.
type box struct {
	offset int
	kind   string
	size   int64 // вместе с заголовком
	header int64
	end    bool // размер ноль: бокс идёт до конца файла
}

// topBoxes перебирает боксы верхнего уровня. Испорченная разметка обрывает
// перебор, а не выдаётся за ошибку: у оборванного файла дальше звука ничего
// нет, и что там за байты — уже не важно.
func topBoxes(data []byte, visit func(box) bool) {
	for offset := 0; offset+8 <= len(data); {
		size := int64(binary.BigEndian.Uint32(data[offset:]))
		current := box{offset: offset, kind: string(data[offset+4 : offset+8]), size: size, header: 8}
		switch {
		case size == 1:
			if offset+16 > len(data) {
				return
			}
			current.size = int64(binary.BigEndian.Uint64(data[offset+8:]))
			current.header = 16
		case size == 0:
			current.size = int64(len(data) - offset)
			current.end = true
		}
		if current.size < current.header {
			return
		}
		if visit(current) {
			return
		}
		if current.end {
			return
		}
		if current.size > int64(len(data)-offset) {
			return
		}
		offset += int(current.size)
	}
}

// HasIndex сообщает, есть ли в файле оглавление moov.
//
// Проверка нужна вызывающему: трогать целые записи нельзя, а отличать их от
// оборванных приходится по файлу, а не по имени.
func HasIndex(data []byte) bool {
	found := false
	topBoxes(data, func(b box) bool {
		if b.kind == "moov" {
			found = true
			return true
		}
		return false
	})
	return found
}

// SampleOffset находит, с какого байта начинается звук.
//
// Это содержимое первого бокса mdat: у оборванного файла его размер равен нулю
// («до конца файла»), и тогда звук — это весь остаток. Оглавление, встреченное
// до звука, означает целый файл: восстанавливать нечего.
func SampleOffset(data []byte) (int, error) {
	offset, found := 0, false
	topBoxes(data, func(b box) bool {
		switch b.kind {
		case "moov":
			found, offset = false, 0
			return true
		case "mdat":
			found, offset = true, b.offset+int(b.header)
			return true
		}
		return false
	})
	if found {
		return offset, nil
	}
	if HasIndex(data) {
		return 0, ErrHasIndex
	}
	return 0, ErrNoSampleData
}
