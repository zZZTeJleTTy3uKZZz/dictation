package conn

import (
	"bytes"
	"encoding/binary"
	"math"
)

// SampleWAV — короткий образец для проверки: тон с плавным началом и концом и
// полсекунды тишины. 16 кГц, моно, 16 бит — тот же формат, в котором диктовка
// пишет и отправляет настоящую запись.
func SampleWAV() []byte {
	const rate = 16000
	const seconds = 0.9
	count := int(rate * seconds)
	samples := make([]int16, count)
	tone := int(rate * 0.4)
	for at := 0; at < tone; at++ {
		// Плавное начало и конец: резкий старт тона — это щелчок, и его
		// распознаватель вправе принять за речь.
		envelope := math.Min(1, math.Min(float64(at)/(rate*0.02), float64(tone-at)/(rate*0.02)))
		samples[at] = int16(6000 * envelope * math.Sin(2*math.Pi*440*float64(at)/rate))
	}
	data := make([]byte, 0, count*2)
	for _, one := range samples {
		data = binary.LittleEndian.AppendUint16(data, uint16(one))
	}

	body := &bytes.Buffer{}
	body.WriteString("RIFF")
	_ = binary.Write(body, binary.LittleEndian, uint32(36+len(data)))
	body.WriteString("WAVE")
	body.WriteString("fmt ")
	_ = binary.Write(body, binary.LittleEndian, uint32(16))
	_ = binary.Write(body, binary.LittleEndian, uint16(1))
	_ = binary.Write(body, binary.LittleEndian, uint16(1))
	_ = binary.Write(body, binary.LittleEndian, uint32(rate))
	_ = binary.Write(body, binary.LittleEndian, uint32(rate*2))
	_ = binary.Write(body, binary.LittleEndian, uint16(2))
	_ = binary.Write(body, binary.LittleEndian, uint16(16))
	body.WriteString("data")
	_ = binary.Write(body, binary.LittleEndian, uint32(len(data)))
	body.Write(data)
	return body.Bytes()
}
