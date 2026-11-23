// Package dictsegment режет звук диктовки на куски по паузам и склеивает
// распознанный текст обратно.
//
// ЗАЧЕМ. Долгая диктовка ждала текста тем дольше, чем дольше говорил человек:
// звук копился в памяти и сжимался и разбирался одним заходом после отпускания
// клавиши. Замер на ПК 16.11.2026: 264 с записи — 4,5 с ожидания, из них 2,8 с
// сжатие. Куски по 20–60 с, уходящие в разбор по ходу речи, убирают и сжатие, и
// облачный запрос из ожидания: при отпускании остаётся только хвост.
//
// ПОЧЕМУ ОТДЕЛЬНЫЙ ПАКЕТ. Диктовка живёт только под Windows (микрофон, служба,
// помощник поля), и её проверки гоняются только там. Нарезка и склейка — это
// арифметика по звуку и строкам, у неё нет ни микрофона, ни сети, ни Windows.
// Здесь она и проверяется там, где идёт go test.
package dictsegment

import (
	"encoding/binary"
	"math"
	"strings"
)

const (
	// Rate — частота диктовки: 16 кГц, моно, S16LE (см. localdict sampleRate).
	Rate = 16000
	// FrameBytes — кадр разбора: 20 мс звука. Кадр — единица всего здесь:
	// пауза, наименьший кусок и наибольший кусок считаются в кадрах.
	FrameBytes = 640
	// pauseFrames — сколько кадров тишины подряд считается паузой: тридцать,
	// то есть 600 мс. Короче — это придыхание между словами, и резать по нему
	// значило бы рвать фразу на части.
	pauseFrames = 30
	// MinBytes — короче двадцати секунд кусок не режем: распознавание такого
	// куска стоит столько же, сколько длинного, а запросов стало бы втрое
	// больше без выигрыша в ожидании.
	MinBytes = 20 * Rate * 2
	// MaxBytes — дольше шестидесяти секунд кусок не держим: пока он ждёт
	// разбора, человек уже говорит следующее. Шестьдесят секунд речи
	// распознаются за секунду-две, и очередь не растёт.
	MaxBytes = 60 * Rate * 2
	// silenceRMS — порог тишины, тот же, что у localdict HasSpeech: звук тише
	// него кадр считается паузой.
	silenceRMS = 0.008
	// PromptLimit — сколько знаков предыдущего текста уходит подсказкой
	// распознавателю. Это не пересказ, а связка: последняя фраза говорит, на
	// чём кончился кусок, и новый кусок не начинается будто с чистого листа.
	PromptLimit = 120
)

// Boundary ищет, где кончить кусок, если он уже готов: длина в байтах или ноль.
//
// Кусок кончается на паузе не короче 600 мс — но только после двадцати секунд
// звука: раннюю паузу пропускаем и ждём следующей. Если человек говорит без
// пауз, кусок обрывается на шестидесяти секундах: ждать паузы, которой нет,
// значит не разбирать ничего до самого отпускания клавиши.
//
// Пауза остаётся в куске (её хвост в шестьсот миллисекунд), как и в прежнем
// NextChunk: так граница не режет слово, а тишина на конце распознавателю не
// мешает.
func Boundary(pcm []byte) int {
	limit := len(pcm)
	if limit > MaxBytes {
		limit = MaxBytes
	}
	if limit < MinBytes {
		return 0
	}
	quiet := 0
	for offset := 0; offset+FrameBytes <= limit; offset += FrameBytes {
		if !quietFrame(pcm[offset:]) {
			quiet = 0
			continue
		}
		quiet++
		if quiet >= pauseFrames && offset+FrameBytes >= MinBytes {
			return offset + FrameBytes
		}
	}
	// Паузы не нашлось, а кусок уже длиннее предела: обрываем по пределу,
	// выравнивая по кадру.
	if len(pcm) >= MaxBytes {
		return MaxBytes
	}
	return 0
}

// quietFrame — тише ли кадр порога тишины. Считается по энергии, как HasSpeech:
// одного числа хватает, чтобы отличить паузу от речи.
//
// Кадр — ровно FrameBytes, даже если на вход отдали хвост длиннее: иначе
// «тишина» считалась бы по всему оставшемуся звуку, а он почти всегда громкий, и
// паузы не нашлись бы вовсе.
func quietFrame(frame []byte) bool {
	if len(frame) > FrameBytes {
		frame = frame[:FrameBytes]
	}
	count := len(frame) / 2
	if count == 0 {
		return true
	}
	var energy float64
	for index := 0; index+1 < len(frame); index += 2 {
		value := float64(int16(binary.LittleEndian.Uint16(frame[index:]))) / 32768
		energy += value * value
	}
	return math.Sqrt(energy/float64(count)) < silenceRMS
}

// Join склеивает тексты кусков в один — в порядке речи.
//
// Пустые куски пропускаются: распознаватель на тишине молчит, и лишний пробел в
// тексте человеку не нужен. Разделитель — один пробел, как у слов внутри куска.
func Join(texts ...string) string {
	parts := make([]string, 0, len(texts))
	for _, text := range texts {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

// PromptTail — хвост предыдущего текста для подсказки распознавателю.
//
// Берётся конец: он про то, что человек говорит сейчас. Начало хвоста обрезается
// по границе слова — обрубок модель достроит как захочет, и в тексте появится
// слово, которого не говорили.
func PromptTail(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" || limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	tail := string(runes[len(runes)-limit:])
	if at := strings.IndexFunc(tail, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }); at >= 0 {
		tail = tail[at:]
	}
	return strings.TrimSpace(tail)
}

// PromptWithContext — подсказка распознавателю: словарь терминов и хвост
// предыдущего текста, вместе и не длиннее предела.
//
// Предел — не наше украшение: у whisper подсказка ограничена двумя сотнями с
// небольшим кусочков, и всё, что дальше, сервис отбрасывает молча или отвергает
// запрос целиком. Поэтому хвост не выводит подсказку за предел: когда словарь
// занимает его весь, хвоста в запросе нет — словарь важнее связности, без него
// имена пишутся вкривь в каждом куске.
func PromptWithContext(hint, tail string, limit int) string {
	hint = strings.TrimSpace(hint)
	tail = strings.TrimSpace(tail)
	if tail == "" || limit <= 0 {
		return hint
	}
	room := limit - len([]rune(hint))
	if hint != "" {
		room-- // пробел между словарём и хвостом
	}
	if room <= 0 {
		return hint
	}
	runes := []rune(tail)
	if len(runes) > room {
		// Берём конец: он про то, что человек говорит сейчас.
		runes = runes[len(runes)-room:]
	}
	tail = strings.TrimSpace(string(runes))
	if hint == "" {
		return tail
	}
	return hint + " " + tail
}

// Seconds — сколько звука в байтах S16LE моно при частоте диктовки.
func Seconds(bytes int) float64 { return float64(bytes) / (Rate * 2) }
