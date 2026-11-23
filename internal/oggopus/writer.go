// Package oggopus пишет и читает Ogg/Opus — контейнер, в котором Ogg-страница
// самодостаточна.
//
// ЗАЧЕМ СВОЙ МУЛЬТИПЛЕКСОР. Opus кодирует звук кусками по 20 мс, а хранить их
// надо в контейнере. MP4 (m4a) держит оглавление в конце: пока файл не закрыт,
// прочитать его нечем — и вылет посреди диктовки оставлял нечитаемый огрызок. В
// Ogg наоборот: заголовок потока (OpusHead, OpusTags) лежит в начале, каждая
// страница несёт свой номер и число отсчётов и читается сама по себе. Оборванный
// в любом месте файл открывается и расшифровывается до последней полной
// страницы.
//
// Пакет знает ровно столько контейнера, сколько нужно записи: страницы Ogg с
// контрольной суммой, OpusHead и OpusTags по RFC 7845. Никаких внешних программ
// и библиотек: всё, что здесь есть, — арифметика и байты.
package oggopus

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// SampleRate — частота, в которой считаются позиции страниц (granulepos).
	// Так велит RFC 7845: даже если звук пришёл с 16 кГц, длина считается на
	// 48 кГц, иначе плеер ошибётся с длительностью втрое.
	SampleRate = 48000
	// MaxSegments — предел оглавления одной страницы: 255 кусков по 255 байт.
	// Больше в формат не влезает.
	MaxSegments = 255
	// maxPageBytes — предел звука на странице (255 × 255).
	maxPageBytes = MaxSegments * 255
	// channelMono — семейство раскладки каналов: 0 значит моно или стерео.
	channelMono = 0
)

// Writer собирает страницы Ogg из готовых кусков Opus. Один Writer — один поток
// (одна запись); писать в него из двух мест нельзя.
type Writer struct {
	w      io.Writer
	serial uint32
	seq    uint32

	preSkip      uint16
	frameSamples uint64 // отсчётов 48 кГц в одном куске

	// Текущая страница: куски и оглавление ещё не записаны.
	segs      []byte
	payload   []byte
	granule   uint64 // позиция конца последнего уложенного куска
	flushedTo uint64 // позиция конца последней записанной страницы
	finished  bool
}

// NewWriter пишет заголовки потока и готов принимать куски.
//
// preSkip — задержка кодека в отсчётах 48 кГц: столько первых отсчётов декодер
// выдаст тишиной, и на столько же запись «длиннее» сказанного, если это не
// записать. frameSamples — сколько отсчётов 48 кГц в одном куске (у кадра 20 мс
// это 960). vendor попадает в OpusTags: по нему видно, чем сделан файл.
func NewWriter(w io.Writer, preSkip uint16, frameSamples int, vendor string) (*Writer, error) {
	if frameSamples <= 0 || frameSamples > int(SampleRate) {
		return nil, fmt.Errorf("ogg: кусок %d отсчётов", frameSamples)
	}
	if preSkip > uint16(SampleRate) {
		return nil, fmt.Errorf("ogg: pre-skip %d отсчётов", preSkip)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	writer := &Writer{w: w, serial: serial, preSkip: preSkip, frameSamples: uint64(frameSamples)}
	head := opusHead(preSkip)
	if err := writer.page(head, 0, true, false); err != nil {
		return nil, err
	}
	if err := writer.page(opusTags(vendor), 0, false, false); err != nil {
		return nil, err
	}
	return writer, nil
}

// Serial — номер потока: его же несут все страницы файла. Нужен проверкам и
// разбору: по нему отличают поток от случайно совпавших байт "OggS" в звуке.
func (w *Writer) Serial() uint32 { return w.serial }

// Write укладывает один готовый кусок Opus. Кусок не копируется между
// страницами: Opus-кадр 20 мс не бывает длиннее 1275 байт и влезает целиком.
func (w *Writer) Write(packet []byte) error {
	if w.finished {
		return errors.New("ogg: запись уже закрыта")
	}
	if len(packet) == 0 {
		return errors.New("ogg: пустой кусок")
	}
	lacing := lacingFor(len(packet))
	// Место кончилось — страницу пора закрывать. Разбивать кусок между
	// страницами можно, но не нужно: запись закрывает страницу раз в секунду, и
	// делить кадр ей не за чем.
	if len(w.segs)+len(lacing) > MaxSegments || len(w.payload)+len(packet) > maxPageBytes {
		if err := w.Flush(); err != nil {
			return err
		}
	}
	w.segs = append(w.segs, lacing...)
	w.payload = append(w.payload, packet...)
	w.granule += w.frameSamples
	return nil
}

// Flush закрывает текущую страницу и записывает её одной записью: страница либо
// целиком на диске, либо её нет. После Flush вызывающий сбрасывает файл на диск
// (Sync) — с этого момента звук переживает и падение питания.
func (w *Writer) Flush() error {
	if len(w.segs) == 0 {
		return nil
	}
	granule := w.granule
	segs, payload := w.segs, w.payload
	w.segs, w.payload = nil, nil
	// Позиция на странице — это конец последнего куска в ней: по ней плеер и
	// считает длительность.
	page := buildPage(0, granule, w.serial, w.seq, segs, payload)
	w.seq++
	if _, err := w.w.Write(page); err != nil {
		return err
	}
	w.flushedTo = granule
	return nil
}

// Close закрывает последнюю страницу пометкой «поток закончен» (EOS). Без неё
// файл всё равно читается — в этом и смысл Ogg, — но пометка избавляет плеер от
// догадок о конце записи.
func (w *Writer) Close() error {
	if w.finished {
		return nil
	}
	w.finished = true
	// Последняя страница несёт и остаток звука, и пометку EOS: отдельная пустая
	// страница только удлинила бы файл.
	if len(w.segs) == 0 {
		// Звука не было вовсе: закрывать нечего, но EOS поставить надо.
		page := buildPage(eosFlag, w.granule, w.serial, w.seq, nil, nil)
		w.seq++
		_, err := w.w.Write(page)
		return err
	}
	granule := w.granule
	segs, payload := w.segs, w.payload
	w.segs, w.payload = nil, nil
	page := buildPage(eosFlag, granule, w.serial, w.seq, segs, payload)
	w.seq++
	if _, err := w.w.Write(page); err != nil {
		return err
	}
	w.flushedTo = granule
	return nil
}

// Granule — отсчётов 48 кГц от начала потока до последнего уложенного куска,
// включая задержку кодека. Разность с pre-skip и есть длина записи.
func (w *Writer) Granule() uint64 { return w.granule }

// page пишет отдельную страницу из одного куска: так ложатся заголовки.
func (w *Writer) page(packet []byte, granule uint64, bos, eos bool) error {
	flags := byte(0)
	if bos {
		flags |= bosFlag
	}
	if eos {
		flags |= eosFlag
	}
	page := buildPage(flags, granule, w.serial, w.seq, lacingFor(len(packet)), packet)
	w.seq++
	_, err := w.w.Write(page)
	return err
}

func lacingFor(size int) []byte {
	segs := make([]byte, 0, size/255+1)
	for size >= 255 {
		segs = append(segs, 255)
		size -= 255
	}
	return append(segs, byte(size))
}

func newSerial() (uint32, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, fmt.Errorf("ogg: номер потока: %w", err)
	}
	return binary.LittleEndian.Uint32(raw[:]), nil
}

const (
	bosFlag    = 0x02
	eosFlag    = 0x04
	noGranule  = ^uint64(0)
	headerSize = 27
	crcField   = 22
)

// buildPage собирает страницу: заголовок, оглавление, звук — и считает
// контрольную сумму по всему этому вместе с обнулённым полем суммы.
func buildPage(flags byte, granule uint64, serial, seq uint32, segs, payload []byte) []byte {
	page := make([]byte, headerSize+len(segs)+len(payload))
	copy(page[0:4], "OggS")
	page[4] = 0 // версия формата
	page[5] = flags
	binary.LittleEndian.PutUint64(page[6:], granule)
	binary.LittleEndian.PutUint32(page[14:], serial)
	binary.LittleEndian.PutUint32(page[18:], seq)
	// page[22:26] — сумма, считается ниже по нулям.
	page[26] = byte(len(segs))
	copy(page[headerSize:], segs)
	copy(page[headerSize+len(segs):], payload)
	crc := checksum(page)
	binary.LittleEndian.PutUint32(page[22:], crc)
	return page
}

// opusHead — заголовок потока по RFC 7845: версия, каналы, задержка кодека,
// частота входа и раскладка каналов.
func opusHead(preSkip uint16) []byte {
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1 // версия
	head[9] = 1 // моно
	binary.LittleEndian.PutUint16(head[10:], preSkip)
	binary.LittleEndian.PutUint32(head[12:], 16000) // частота входа: сам Opus на 48 кГц
	binary.LittleEndian.PutUint16(head[16:], 0)     // усиление
	head[18] = channelMono
	return head
}

// opusTags — сведения о том, чем файл сделан. Пустым его оставлять не стоит:
// именно по нему видно, что запись наша, а не чужая.
func opusTags(vendor string) []byte {
	comments := []string{"ENCODER=Dictation"}
	if vendor == "" {
		vendor = "meetdigest-win-go"
	}
	tags := make([]byte, 0, 16+len(vendor)+32)
	tags = append(tags, "OpusTags"...)
	tags = binary.LittleEndian.AppendUint32(tags, uint32(len(vendor)))
	tags = append(tags, vendor...)
	tags = binary.LittleEndian.AppendUint32(tags, uint32(len(comments)))
	for _, comment := range comments {
		tags = binary.LittleEndian.AppendUint32(tags, uint32(len(comment)))
		tags = append(tags, comment...)
	}
	return tags
}

// checksum — контрольная сумма Ogg: полином 0x04c11db7, без отражения и без
// итогового исключающего или. Поле суммы (байты 22–25) в расчёт не идёт: и при
// записи, и при проверке на его месте нули.
func checksum(page []byte) uint32 {
	var crc uint32
	for index, one := range page {
		if index >= crcField && index < crcField+4 {
			one = 0
		}
		crc = (crc << 8) ^ crcTable[byte(crc>>24)^one]
	}
	return crc
}

var crcTable = func() [256]uint32 {
	var table [256]uint32
	for i := 0; i < 256; i++ {
		value := uint32(i) << 24
		for bit := 0; bit < 8; bit++ {
			if value&0x80000000 != 0 {
				value = value<<1 ^ 0x04c11db7
			} else {
				value <<= 1
			}
		}
		table[i] = value
	}
	return table
}()
