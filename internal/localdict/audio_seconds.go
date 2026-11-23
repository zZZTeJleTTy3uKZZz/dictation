package localdict

import (
	"bytes"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/opusrec"
)

// audioSecondsOf — сколько в записи речи, по её же байтам.
//
// У WAV длина известна точно: звук лежит отсчётами, по два байта на отсчёт.
// У сжатой записи длины в самом файле может не быть — её пишут не все
// кодировщики, — поэтому она читается из контейнера: каждый кусок звука помечен
// своим отсчётом от начала, и отсчёт последнего куска и есть длина записи.
//
// Так у Opus в Ogg (наша запись и запись Harness): позиция последней страницы.
// Так же у Opus в WebM: кусок у MediaRecorder длится около тридцати секунд, и
// замер живой записи это подтвердил — 60 105 мс при длине 60,1 с.
//
// Прежде длина сжатой записи считалась той же меркой, что у WAV, и выходила в
// разы меньше правды: минута речи показывалась в истории как пять секунд.
func audioSecondsOf(body []byte) float64 {
	extension, known := recordingFormat(body)
	if !known {
		return 0
	}
	switch extension {
	case ".wav":
		return float64(len(body)) / (2 * speechRate)
	case ".ogg":
		if seconds, ok := opusrec.SecondsOf(body); ok {
			return seconds
		}
		return 0
	}
	return webmSeconds(body)
}

// webmSeconds — длина записи по отсчёту её последнего куска.
func webmSeconds(body []byte) float64 {
	mark := []byte{0x1f, 0x43, 0xb6, 0x75} // начало куска
	at := bytes.LastIndex(body, mark)
	for at >= 0 {
		if ms, ok := clusterTimecode(body[at+len(mark):]); ok {
			return float64(ms) / 1000
		}
		// Четыре байта начала куска могут случайно совпасть со звуком. Идём
		// назад, пока не найдём кусок, внутри которого действительно отсчёт.
		at = bytes.LastIndex(body[:at], mark)
	}
	return 0
}

// clusterTimecode — отсчёт внутри куска.
//
// Замер живой записи (договор диктовки, 15.10.2026): сперва идёт длина куска —
// восемь байт, — а первым же байтом внутри стоит метка отсчёта. Поэтому длина
// разбирается числом переменной длины любой длины: прежний образец с одним
// байтом этой дороги не проверял вовсе, и читатель падал на живой записи.
func clusterTimecode(data []byte) (int, bool) {
	size, read, ok := vint(data)
	if !ok {
		return 0, false
	}
	end := read + size
	if end > len(data) {
		end = len(data)
	}
	inside := data[read:end]
	// Отсчёт стоит первым. Поиск по всему куску оставлен на случай, если
	// кодировщик положит перед ним что-то ещё.
	for i := 0; i+2 < len(inside); i++ {
		if inside[i] != 0xe7 {
			continue
		}
		length, after, ok := vint(inside[i+1:])
		if !ok || length <= 0 || i+1+after+length > len(inside) {
			continue
		}
		value := 0
		for _, one := range inside[i+1+after : i+1+after+length] {
			value = value<<8 | int(one)
		}
		return value, true
	}
	return 0, false
}

// vint — число переменной длины: так размечены величины в EBML. Старшие биты
// первого байта говорят, сколько байтов занято, и они же в число не входят.
func vint(data []byte) (value int, read int, ok bool) {
	if len(data) == 0 {
		return 0, 0, false
	}
	mask := byte(0x80)
	length := 1
	for data[0]&mask == 0 {
		mask >>= 1
		length++
		if mask == 0 {
			return 0, 0, false
		}
	}
	if length > 8 || len(data) < length {
		return 0, 0, false
	}
	value = int(data[0] & (mask - 1))
	for i := 1; i < length; i++ {
		value = value<<8 | int(data[i])
	}
	return value, length, true
}
