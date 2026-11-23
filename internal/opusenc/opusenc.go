//go:build cgo

// Package opusenc is the Opus codec inside the process: it turns sixteen
// kilohertz mono speech into Opus packets and back.
//
// ЗАЧЕМ СВОЙ КОДЕК. Media Foundation, которым сжимала звук прежняя запись,
// Opus-кодировщика не имеет вовсе, а внешняя программа (audio-compress.exe)
// писала AAC в MP4: там оглавление ложится в конец файла, поэтому до закрытия
// файла на диске не было звука, а вылет посреди диктовки оставлял нечитаемый
// огрызок. Opus в Ogg устроен наоборот: каждая страница самодостаточна, и
// оборванный файл читается до последней сброшенной страницы.
//
// ПОЧЕМУ CGO. Чистого Go-кодировщика Opus нет: есть декодер (pion/opus), а
// кодировщик — это libopus, проверенный годами код Xiph. Он собирается вместе с
// пакетом из исходников в csrc (BSD, см. csrc/COPYING), поэтому на машине ничего
// ставить не нужно: ни библиотеки, ни внешней программы. Это тот же путь, каким
// уже линкуется sherpa-onnx, только без готовых двоичных файлов — исходники
// компилирует тот же mingw, что и остальной cgo этого проекта.
//
// Вход — 16 кГц S16, как с микрофона: Opus внутри работает на 48 кГц и
// пересэмплирует сам, а 48 кГц на входе дали бы только лишнюю работу и лишние
// байты в истории. Выход — кадры по 20 мс, 40 кбит/с VBR.
package opusenc

/*
#cgo CFLAGS: -O2 -DOPUS_BUILD -DHAVE_CONFIG_H -DVAR_ARRAYS -Wno-stringop-overread -I${SRCDIR}/csrc -I${SRCDIR}/csrc/include -I${SRCDIR}/csrc/celt -I${SRCDIR}/csrc/silk -I${SRCDIR}/csrc/silk/float -I${SRCDIR}/csrc/src
#cgo !windows LDFLAGS: -lm
#include <stdlib.h>
#include "opus.h"

// Обёртки для значений, которых нет в opus.h как констант: cgo видит макросы
// только через функции, а OPUS_SET_BITRATE и OPUS_APPLICATION_VOIP — макросы.
static int opusenc_set_bitrate(OpusEncoder *st, opus_int32 bitrate) {
	return opus_encoder_ctl(st, OPUS_SET_BITRATE(bitrate));
}
static int opusenc_set_vbr(OpusEncoder *st, opus_int32 vbr) {
	return opus_encoder_ctl(st, OPUS_SET_VBR(vbr));
}
static int opusenc_lookahead(OpusEncoder *st) {
	opus_int32 value = 0;
	if (opus_encoder_ctl(st, OPUS_GET_LOOKAHEAD(&value)) != OPUS_OK) {
		return -1;
	}
	return value;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

const (
	// Rate — частота входа: столько отдаёт микрофон и столько же ждёт
	// распознаватель.
	Rate = 16000
	// Channels — моно: диктовка это один голос, стерео здесь нечего делить.
	Channels = 1
	// FrameMillis — кадр Opus. Двадцать миллисекунд — наименьшая задержка,
	// которую Opus отдаёт без потерь в сжатии.
	FrameMillis = 20
	// FrameSamples — отсчётов входа в одном кадре (320 при 16 кГц).
	FrameSamples = Rate * FrameMillis / 1000
	// Bitrate — 40 кбит/с: час речи занимает около 18 МБ.
	Bitrate = 40000
	// packetBytes — с запасом на самый большой кадр Opus (1275 байт при
	// 510 кбит/с): буфер не пересоздаётся на каждый кадр.
	packetBytes = 1500
)

// Encoder — один кодировщик на одну запись.
type Encoder struct {
	enc   *C.OpusEncoder
	out   []byte
	inbuf []C.opus_int16
}

// NewEncoder готовит кодировщик речи: 16 кГц, моно, 40 кбит/с, переменный
// битрейт. VBR выбран не для экономии: на паузах и тихой речи Opus отдаёт
// меньше байт, а на согласных больше, и при том же среднем звучит лучше
// постоянного.
func NewEncoder() (*Encoder, error) {
	var code C.int
	enc := C.opus_encoder_create(Rate, Channels, C.OPUS_APPLICATION_VOIP, &code)
	if enc == nil || code != C.OPUS_OK {
		return nil, fmt.Errorf("opus: кодировщик не создан (код %d)", int(code))
	}
	if err := ctl(C.opusenc_set_bitrate(enc, Bitrate)); err != nil {
		C.opus_encoder_destroy(enc)
		return nil, err
	}
	if err := ctl(C.opusenc_set_vbr(enc, 1)); err != nil {
		C.opus_encoder_destroy(enc)
		return nil, err
	}
	return &Encoder{
		enc:   enc,
		out:   make([]byte, packetBytes),
		inbuf: make([]C.opus_int16, FrameSamples),
	}, nil
}

// Lookahead — задержка кодека в отсчётах 48 кГц. Столько первых отсчётов
// декодер выдаст тишиной, и ровно это число уходит в OpusHead полем pre-skip:
// без него длительность любой записи была бы завышена на эти миллисекунды.
func (e *Encoder) Lookahead() int {
	samples := int(C.opusenc_lookahead(e.enc))
	if samples < 0 {
		return 0
	}
	// libopus отдаёт задержку в отсчётах своей частоты входа, а granulepos в
	// Ogg считается на 48 кГц.
	return samples * 48000 / Rate
}

// Encode сжимает один кадр: pcm — 320 отсчётов S16LE (640 байт). Возвращённый
// кусок живёт до следующего вызова: копировать его незачем, запись сразу
// укладывает его в страницу Ogg.
func (e *Encoder) Encode(pcm []byte) ([]byte, error) {
	if len(pcm) != FrameSamples*2 {
		return nil, fmt.Errorf("opus: кадр %d байт, ждали %d", len(pcm), FrameSamples*2)
	}
	for i := 0; i < FrameSamples; i++ {
		e.inbuf[i] = C.opus_int16(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8))
	}
	n := C.opus_encode(e.enc, &e.inbuf[0], C.int(FrameSamples), (*C.uchar)(unsafe.Pointer(&e.out[0])), C.opus_int32(len(e.out)))
	if n < 0 {
		return nil, fmt.Errorf("opus: кодирование отказало (код %d)", int(n))
	}
	return e.out[:int(n)], nil
}

// Close освобождает кодировщик. Повторный вызов безопасен.
func (e *Encoder) Close() {
	if e.enc != nil {
		C.opus_encoder_destroy(e.enc)
		e.enc = nil
	}
}

// Decoder — обратный ход: кусок Opus в отсчёты. Нужен проверкам (разобрать
// оборванную запись нечем, если не уметь декодировать) и местному
// распознавателю, который ждёт несжатый звук.
type Decoder struct {
	dec *C.OpusDecoder
	buf []C.opus_int16
}

// NewDecoder готовит декодер на частоте rate. Opus внутри всегда 48 кГц, но
// декодер сам пересэмплирует в 16 кГц — распознавателю нужна его частота.
func NewDecoder(rate int) (*Decoder, error) {
	var code C.int
	dec := C.opus_decoder_create(C.opus_int32(rate), Channels, &code)
	if dec == nil || code != C.OPUS_OK {
		return nil, fmt.Errorf("opus: декодер не создан (код %d)", int(code))
	}
	return &Decoder{dec: dec, buf: make([]C.opus_int16, 5760)}, nil
}

// Decode разворачивает один кусок в отсчёты S16LE. Кадр 20 мс — это 320
// отсчётов на 16 кГц; буфер рассчитан на самый длинный кадр Opus (120 мс).
// Пропуск в потоке (потерянная страница) декодируется как тишина: так
// оборванная запись не превращается в отказ целиком.
func (d *Decoder) Decode(packet []byte, pcm []byte) (int, error) {
	if len(packet) == 0 {
		return 0, errors.New("opus: пустой кусок")
	}
	n := C.opus_decode(d.dec, (*C.uchar)(unsafe.Pointer(&packet[0])), C.opus_int32(len(packet)),
		&d.buf[0], C.int(len(d.buf)), 0)
	if n < 0 {
		return 0, fmt.Errorf("opus: декодирование отказало (код %d)", int(n))
	}
	for i := 0; i < int(n); i++ {
		if len(pcm) < 2*int(n) {
			return 0, errors.New("opus: мало места под отсчёты")
		}
		value := uint16(d.buf[i])
		pcm[2*i] = byte(value)
		pcm[2*i+1] = byte(value >> 8)
	}
	return int(n), nil
}

// Close освобождает декодер. Повторный вызов безопасен.
func (d *Decoder) Close() {
	if d.dec != nil {
		C.opus_decoder_destroy(d.dec)
		d.dec = nil
	}
}

// Version — версия libopus, собранной в этот двоичный файл. Нужна журналу и
// проверке: по ней видно, что в бою именно та библиотека, о которой написано в
// MR, а не оставшаяся на машине старая.
func Version() string {
	return C.GoString(C.opus_get_version_string())
}

func ctl(code C.int) error {
	if code != C.OPUS_OK {
		return fmt.Errorf("opus: настройка отказала (код %d)", int(code))
	}
	return nil
}
