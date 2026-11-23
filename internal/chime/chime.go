// Package chime синтезирует короткие звуки плашки диктовки: начало записи,
// остановку, «вставлено» и «в буфере обмена».
//
// Звуки считаются, а не лежат файлами: так они не теряются при установке и
// одинаковы на всех машинах. Каждый — синус с парой тихих обертонов, быстрой
// атакой и плавным затуханием: мягкий щелчок-колокольчик, а не системный
// писк. Громкость низкая нарочно — звук подтверждает действие, а не зовёт.
package chime

import (
	"bytes"
	"encoding/binary"
	"math"
)

const sampleRate = 44100

// note — одна нота звука: частота, начало, длина затухания и громкость.
type note struct {
	freq, at, decay, gain float64
}

// render складывает ноты в 16-битный моно WAV.
func render(notes []note) []byte {
	length := 0.0
	for _, n := range notes {
		length = math.Max(length, n.at+n.decay*5)
	}
	count := int(length * sampleRate)
	samples := make([]float64, count)
	for _, n := range notes {
		start := int(n.at * sampleRate)
		for i := start; i < count; i++ {
			t := float64(i-start) / sampleRate
			// Атака 4 мс — без щелчка в начале, затухание экспонентой.
			envelope := math.Min(1, t/.004) * math.Exp(-t/n.decay)
			tone := math.Sin(2*math.Pi*n.freq*t) +
				.22*math.Sin(2*math.Pi*n.freq*2*t)*math.Exp(-t/(n.decay*.5)) +
				.06*math.Sin(2*math.Pi*n.freq*3*t)*math.Exp(-t/(n.decay*.3))
			samples[i] += n.gain * envelope * tone
		}
	}
	return wav(samples)
}

// bubble — мягкий «буп»: чистый синус, тон которого плавно скользит от from
// к to (за rise секунд — на две трети пути) и гаснет за decay. Скольжение
// нарочно медленное и короткое по высоте: быстрый взлёт на октаву звучит
// писком, а это должно звучать округло.
type bubble struct {
	from, to, at, rise, decay, gain float64
}

// attackTime — нарастание звука: по косинусу, без щелчка и без «удара».
const attackTime = .008

// renderBubbles складывает бупы в WAV. Частота меняется плавно, поэтому фаза
// набирается шагами, а не считается по формуле синуса от времени.
func renderBubbles(bubbles []bubble) []byte {
	length := 0.0
	for _, b := range bubbles {
		length = math.Max(length, b.at+b.decay*6)
	}
	count := int(length * sampleRate)
	samples := make([]float64, count)
	for _, b := range bubbles {
		start := int(b.at * sampleRate)
		phase := 0.0
		for i := start; i < count; i++ {
			t := float64(i-start) / sampleRate
			freq := b.from + (b.to-b.from)*(1-math.Exp(-t/b.rise))
			phase += 2 * math.Pi * freq / sampleRate
			attack := 1.0
			if t < attackTime {
				attack = .5 - .5*math.Cos(math.Pi*t/attackTime)
			}
			samples[i] += b.gain * attack * math.Exp(-t/b.decay) * math.Sin(phase)
		}
	}
	return wav(samples)
}

// Start — запись началась: «буп» с лёгким подъёмом тона.
func Start() []byte {
	return renderBubbles([]bubble{{from: 600, to: 700, at: 0, rise: .04, decay: .07, gain: .3}})
}

// Stop — запись остановлена: тот же «буп» с лёгким спуском.
func Stop() []byte {
	return renderBubbles([]bubble{{from: 700, to: 590, at: 0, rise: .04, decay: .07, gain: .28}})
}

// Silence — короткая тишина: ею звук разогревается заранее, чтобы первое
// настоящее проигрывание не ждало открытия звукового устройства.
func Silence() []byte { return wav(make([]float64, sampleRate/50)) }

// Done — текст на месте: светлый «динь» с квинтой поверх.
func Done() []byte {
	return render([]note{{2093, 0, .11, .13}, {3136, .012, .09, .06}})
}

// Clipboard — текст в буфере: одна ровная средняя нота, без радости, но и
// без тревоги.
func Clipboard() []byte {
	return render([]note{{1046.5, 0, .09, .15}})
}

// wav — отсчёты в 16-битный моно WAV. Хвост гасится в ноль за 10 мс: обрыв
// посреди колебания щёлкает.
func wav(samples []float64) []byte {
	count := len(samples)
	fade := int(.01 * sampleRate)
	for i := 0; i < fade && i < count; i++ {
		samples[count-1-i] *= float64(i) / float64(fade)
	}
	var data bytes.Buffer
	for _, s := range samples {
		_ = binary.Write(&data, binary.LittleEndian, int16(math.Max(-1, math.Min(1, s))*32767))
	}
	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(36+data.Len()))
	out.WriteString("WAVEfmt ")
	// Формат: PCM, один канал, 44,1 кГц, 16 бит.
	for _, field := range []any{uint32(16), uint16(1), uint16(1), uint32(sampleRate), uint32(sampleRate * 2), uint16(2), uint16(16)} {
		_ = binary.Write(&out, binary.LittleEndian, field)
	}
	out.WriteString("data")
	_ = binary.Write(&out, binary.LittleEndian, uint32(data.Len()))
	out.Write(data.Bytes())
	return out.Bytes()
}
