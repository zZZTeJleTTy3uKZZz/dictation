package opusrec

import (
	"fmt"
	"os"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/oggopus"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/opusenc"
)

// Probe читает файл и рассказывает, что в нём: сколько звука, цел ли хвост,
// закрыт ли поток. Длительность берётся по последней целой странице, поэтому у
// оборванной записи она честная — до того места, где звук успел лечь на диск.
func Probe(path string) (oggopus.Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return oggopus.Info{}, err
	}
	return oggopus.Inspect(data)
}

// Seconds — длина записи в секундах. ok=false значит, что файл не Ogg/Opus или
// в нём нет ни одной целой страницы: вызывающий решает сам, что это означает.
func Seconds(path string) (float64, bool) {
	info, err := Probe(path)
	if err != nil {
		return 0, false
	}
	return info.Seconds(), true
}

// SecondsOf — то же для звука, который пришёл куском в память, а не лежит
// файлом: чужая программа присылает запись телом запроса.
func SecondsOf(body []byte) (float64, bool) {
	info, err := oggopus.Inspect(body)
	if err != nil {
		return 0, false
	}
	return info.Seconds(), true
}

// Decode разворачивает запись в звук для распознавателя: 16 кГц, моно, S16LE.
//
// ЗАЧЕМ РАЗВОРАЧИВАТЬ. Облако принимает наш файл как есть, а помощнику сжатия и
// восстановлению старых записей нужен несжатый звук. Оборванный хвост
// декодируется до последней целой страницы: разговор, оборвавшийся вылетом, всё
// равно расшифровывается.
func Decode(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	packets, err := oggopus.Packets(data)
	if err != nil {
		return nil, err
	}
	if len(packets) == 0 {
		return nil, fmt.Errorf("opusrec: в записи нет звука")
	}
	decoder, err := opusenc.NewDecoder(opusenc.Rate)
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	frame := make([]byte, opusenc.FrameSamples*2)
	pcm := make([]byte, 0, len(packets)*len(frame))
	// Кусок, который не развернулся, не обрывает разбор: пропуск в потоке
	// означает потерю двадцати миллисекунд, а не всей записи.
	for _, packet := range packets {
		n, err := decoder.Decode(packet, frame)
		if err != nil {
			continue
		}
		pcm = append(pcm, frame[:n*2]...)
	}
	if len(pcm) == 0 {
		return nil, fmt.Errorf("opusrec: запись не развернулась ни на кадр")
	}
	return pcm, nil
}
