package oggopus

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// ErrNotOgg — файл начинается не с Ogg-страницы.
var ErrNotOgg = errors.New("ogg: это не Ogg")

// Page — одна страница Ogg с уже разобранным оглавлением.
type Page struct {
	Flags   byte
	Granule uint64
	Serial  uint32
	Seq     uint32
	// Packets — куски, которые закончились на этой странице. Наша запись куски
	// между страницами не делит, поэтому недописанный кусок сюда не попадает.
	Packets [][]byte
}

// Info — что видно в файле Ogg/Opus по его байтам.
type Info struct {
	Serial   uint32
	Channels int
	PreSkip  uint16
	// Granule — позиция конца последней ЦЕЛОЙ страницы. По ней считается длина
	// записи: вычесть pre-skip и разделить на 48 кГц.
	Granule uint64
	// Packets — сколько кусков звука дошло до целых страниц.
	Packets int
	// Pages — сколько страниц прочитано (два заголовка входят).
	Pages int
	// Bytes — сколько байт файла занято целыми страницами. Всё, что дальше, —
	// оборванный хвост.
	Bytes int64
	// Finished — файл закрыт пометкой EOS. У оборванной записи её нет, и это не
	// поломка: звук читается и без неё.
	Finished bool
	// Truncated — в конце остался неполный или битый хвост: так выглядит
	// страница, которую процесс не успел дописать. Всё до неё цело.
	Truncated bool
}

// Seconds — длина записи по последней целой странице. Задержка кодека
// вычитается: без этого каждая запись была бы на несколько миллисекунд длиннее
// сказанного.
func (i Info) Seconds() float64 {
	if i.Granule <= uint64(i.PreSkip) {
		return 0
	}
	return float64(i.Granule-uint64(i.PreSkip)) / SampleRate
}

// Duration — то же, что Seconds, но временем.
func (i Info) Duration() time.Duration {
	return time.Duration(i.Seconds() * float64(time.Second))
}

// Inspect разбирает файл до конца и проверяет контрольную сумму каждой
// страницы. Битый или недописанный хвост ошибкой не считается: он
// отбрасывается, а всё, что до него, остаётся годным — ради этого формат и
// выбран.
func Inspect(data []byte) (Info, error) {
	var info Info
	rest := data
	for len(rest) > 0 {
		if len(rest) < headerSize || string(rest[0:4]) != "OggS" {
			info.Truncated = true
			break
		}
		page, tail, err := nextPage(rest)
		if err != nil {
			return info, err
		}
		if page == nil {
			info.Truncated = true
			break
		}
		if info.Pages == 0 {
			if page.Flags&bosFlag == 0 {
				return info, errors.New("ogg: первая страница без пометки начала потока")
			}
			info.Serial = page.Serial
			if len(page.Packets) == 0 || !hasPrefix(page.Packets[0], "OpusHead") {
				return info, errors.New("ogg: поток не Opus (нет OpusHead)")
			}
			head := page.Packets[0]
			if len(head) < 19 {
				return info, errors.New("ogg: OpusHead короче заголовка")
			}
			info.Channels = int(head[9])
			info.PreSkip = binary.LittleEndian.Uint16(head[10:])
		} else if !hasPrefixPackets(page.Packets, "OpusTags") {
			info.Packets += len(page.Packets)
			if page.Granule != noGranule {
				info.Granule = page.Granule
			}
			if page.Flags&eosFlag != 0 {
				info.Finished = true
			}
		}
		info.Pages++
		info.Bytes += int64(len(rest) - len(tail))
		rest = tail
	}
	if info.Pages == 0 {
		return info, ErrNotOgg
	}
	return info, nil
}

// Packets отдаёт куски звука в том порядке, в каком они лежат в файле.
// Заголовки (OpusHead, OpusTags) в ответ не входят, недописанный хвост
// отбрасывается.
func Packets(data []byte) ([][]byte, error) {
	var packets [][]byte
	pages := 0
	rest := data
	for len(rest) > 0 {
		if len(rest) < headerSize || string(rest[0:4]) != "OggS" {
			break
		}
		page, tail, err := nextPage(rest)
		if err != nil {
			return nil, err
		}
		if page == nil {
			break
		}
		pages++
		if pages > 2 {
			packets = append(packets, page.Packets...)
		}
		rest = tail
	}
	if pages == 0 {
		return nil, ErrNotOgg
	}
	return packets, nil
}

// nextPage отдаёт одну страницу и остаток файла. nil вместо страницы значит,
// что остаток — недописанный хвост, а не ошибка.
func nextPage(rest []byte) (*Page, []byte, error) {
	if len(rest) < headerSize {
		return nil, nil, nil
	}
	if string(rest[0:4]) != "OggS" {
		return nil, nil, ErrNotOgg
	}
	if rest[4] != 0 {
		return nil, nil, fmt.Errorf("ogg: версия формата %d", rest[4])
	}
	segments := int(rest[26])
	if len(rest) < headerSize+segments {
		return nil, nil, nil
	}
	size := 0
	for _, one := range rest[headerSize : headerSize+segments] {
		size += int(one)
	}
	total := headerSize + segments + size
	if len(rest) < total {
		return nil, nil, nil
	}
	raw := rest[:total]
	if checksum(raw) != binary.LittleEndian.Uint32(raw[22:]) {
		return nil, nil, nil
	}
	page := &Page{
		Flags:   raw[5],
		Granule: binary.LittleEndian.Uint64(raw[6:]),
		Serial:  binary.LittleEndian.Uint32(raw[14:]),
		Seq:     binary.LittleEndian.Uint32(raw[18:]),
		Packets: pagePackets(raw),
	}
	return page, rest[total:], nil
}

// pagePackets разбирает оглавление страницы на куски: сегмент короче 255 значит
// конец куска, 255 — кусок продолжается на этой же странице.
func pagePackets(page []byte) [][]byte {
	segments := int(page[26])
	lacing := page[headerSize : headerSize+segments]
	payload := page[headerSize+segments:]
	var packets [][]byte
	var current []byte
	offset := 0
	for _, one := range lacing {
		end := offset + int(one)
		if end > len(payload) {
			end = len(payload)
		}
		current = append(current, payload[offset:end]...)
		offset = end
		if one < 255 {
			packets = append(packets, current)
			current = nil
		}
	}
	// Недописанный кусок (продолжение на следующей странице) не отдаём: наша
	// запись куски не делит, и половина кадра — не кадр.
	return packets
}

func hasPrefix(data []byte, prefix string) bool {
	return len(data) >= len(prefix) && string(data[:len(prefix)]) == prefix
}

// hasPrefixPackets — начало первого куска страницы.
func hasPrefixPackets(packets [][]byte, prefix string) bool {
	return len(packets) > 0 && hasPrefix(packets[0], prefix)
}
