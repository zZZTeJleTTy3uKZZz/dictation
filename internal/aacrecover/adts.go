package aacrecover

// AudioSpecificConfig известен заранее: звук пишет наш же помощник — AAC-LC,
// 16 кГц, моно. Эти три числа и есть весь заголовок ADTS.
const (
	// FrameSamples — сколько отсчётов отдаёт декодер за один кадр AAC-LC.
	FrameSamples = 1024
	// SampleRate — частота записи диктовки.
	SampleRate = 16000
	// sampleRateIndex — номер 16 кГц в таблице частот ADTS.
	sampleRateIndex = 8
	// adtsHeaderBytes — длина заголовка ADTS без контрольной суммы.
	adtsHeaderBytes = 7
)

// Wrap заворачивает сырые байты в ADTS-заголовок.
//
// Зачем вообще заворачивать: ffmpeg не берёт сырые кадры вовсе — он ждёт ADTS,
// где длина записана в заголовке. Заголовок ставится не на каждый кадр, а на
// кусок до 8191 байта: внутри него лежит несколько кадров подряд, и декодер
// читает их все. Длина куска ограничена тринадцатью битами заголовка.
// Заголовок целиком выводится из параметров записи — AAC-LC, 16 кГц, моно.
func Wrap(frame []byte) []byte {
	total := len(frame) + adtsHeaderBytes
	out := make([]byte, total)
	out[0] = 0xFF
	// MPEG-4, слой 0, без контрольной суммы: тот же набор, что пишет помощник.
	out[1] = 0xF1
	// profile 1 — AAC-LC, номер частоты, старший бит каналов.
	out[2] = (1 << 6) | (sampleRateIndex << 2) | 0
	// младший бит каналов (моно=1 — это нули), старшие два бита длины.
	out[3] = (1 << 6) | byte((total>>11)&0x03)
	out[4] = byte((total >> 3) & 0xFF)
	// младшие три бита длины, «буфер полон», остаток заголовка.
	out[5] = byte((total&0x07)<<5) | 0x1F
	out[6] = 0xFC
	copy(out[adtsHeaderBytes:], frame)
	return out
}
