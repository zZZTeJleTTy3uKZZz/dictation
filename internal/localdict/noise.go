package localdict

import (
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

// errNoisyMicrophone — вход отдаёт шум вместо речи. Слова читаются человеком,
// и в них есть и что случилось, и куда идти.
var errNoisyMicrophone = errors.New("Микрофон даёт шум — проверь вход звука (Параметры → Звук → Ввод). Запись сохранена в истории, её можно переотправить.")

// Защита от испорченного звука: перед отправкой фразу надо посмотреть, а не
// только взвесить.
//
// Микрофон может отдать не речь, а ровный шум — так было 03.11.2026 в 03:13:
// запись длиной сорок четыре секунды, громкая (RMS 0,068), с плоским спектром
// на всех частотах. Распознаватель на таком звуке либо молчит, либо выдумывает
// слова, а человек ждёт текста и не понимает, почему его нет. Отправить шум —
// это ещё и запрос в облако, за который платят.
//
// ЧТО СЧИТАЕТСЯ. У речи энергия собрана внизу: основной тон и его гармоники
// лежат в первых сотнях герц, и даже у шипящих выше двух килогерц её немного.
// У шума энергии примерно поровну на всех частотах, и спектральная плоскость
// (отношение среднего геометрического к среднему арифметическому по полосам)
// у него близка к единице. Поэтому решение принимается по двум числам сразу:
//
//   - спектральная плоскость активных кадров: у шума она высокая;
//   - доля энергии ниже 500 Гц: у речи она большая, у шума — нет.
//
// Двух чисел, а не одного, потому что каждое по отдельности ошибается: тихая
// шипящая речь даёт высокую плоскость, а низкий гул вентилятора — большую долю
// низких частот. Вместе они расходятся на образцах с запасом (см. пороги).
//
// ЧЕМ ИЗМЕРЕНО. Два образца с рабочей машины, оба 16 кГц моно:
//
//	noise-20261103-031318.m4a — шум: плоскость 0,26…0,33, низких 0,45…0,58;
//	ok-20261102-234909.m4a    — речь: плоскость 0,02…0,20, низких 0,74…0,90.
//
// Числа — по двухсекундным окнам, то есть с запасом на короткую диктовку.
// Пороги стоят посередине: 0,22 по плоскости и 0,65 по низким частотам.
//
// ПАУЗА — НЕ ШУМ. Тихая комната дала другой случай (03.11.2026, журнал
// ноутбука владельца): кусок в пять-десять секунд с RMS 0,001, плоскостью 0,599,
// низкими 0,044 и восемью звучащими кадрами. Спектр шипения — шумовой, но уровень
// в шестнадцать раз ниже настоящего шума (RMS 0,016 с плоским спектром). Поэтому
// уровень разделяет тишину и шум: ниже soundSilentRMS (0,004, середина между
// 0,001 и 0,016 по логарифмической шкале) шумоподобный звук — тишина, выше —
// шум. Тихая РЕЧЬ отличается спектром, а не громкостью: у речи энергия собрана в
// основном тоне и гармониках ниже 500 Гц, у шипения — размазана по всей полосе.
// Поэтому тихое слово в тишине со шипением в тишину не попадает (см. Silent).

const (
	// soundFrame и soundHop — кадр и шаг разбора: 32 мс и 16 мс при 16 кГц.
	// Кадр в степень двойки выбран ради быстрого преобразования Фурье.
	soundFrame = 512
	soundHop   = 256
	// soundActiveRMS — с какого уровня кадр считается звучащим. Тот же порог,
	// что у HasSpeech: тишина в оценке не участвует, иначе паузы между словами
	// разбавили бы картину речи.
	soundActiveRMS = 0.008
	// soundFlatness — выше этой плоскости звук считается шумом.
	soundFlatness = 0.22
	// soundLowBand — ниже этой доли энергии в первых 500 Гц звук считается
	// шумом. У речи эта доля всегда больше.
	soundLowBand = 0.65
	// soundLowBandEdge — где кончается «низ»: 500 Гц.
	soundLowBandEdge = 500.0
	// soundMinVoiced — сколько звучащих кадров нужно, чтобы вообще судить.
	// Меньше — это не фраза, а щелчок; такой звук отсекает HasSpeech.
	soundMinVoiced = 6
	// soundClipLevel — с какого уровня отсчёт считается упёршимся в предел.
	soundClipLevel = 0.98
	// soundEventRMS — пик кадра, с которого звук — громкое событие, а не тишина.
	// Шипение из журнала ноутбука (03.11.2026) имело звучащие кадры чуть выше
	// порога звучащего кадра 0,008; ясное слово даёт кадры выше 0,02. Громкое
	// событие с плоским спектром (шёпот, шипящее) тишиной не бывает: решает облако.
	soundEventRMS = 0.02
	// soundSilentRMS — ниже этого общего уровня звук — тишина с шипением, а не
	// шум микрофона и не речь. Числа — в комментарии выше: 0,001 из журнала
	// тихой комнаты и 0,016 настоящего шума.
	soundSilentRMS = 0.004
)

// SoundQuality — что показал осмотр фразы. Числа остаются в журнале: по ним
// видно, почему фраза не ушла в разбор, и с чем сравнивать следующий случай.
type SoundQuality struct {
	RMS      float64
	Flatness float64
	LowBand  float64
	Clipping float64
	// Voiced — звучащие кадры (громче soundActiveRMS); Peak — самый громкий кадр
	// (наибольший RMS среди кадров 512 отсчётов). Пик отделяет громкое событие с
	// плоским спектром от тишины: общий RMS такое событие разбавляет.
	Voiced int
	Peak   float64
}

// spectrumNoise — спектр шумоподобный: звучащих кадров достаточно, плоскость
// высокая, низких частот мало. Речь так не выглядит: её энергия собрана в
// основном тоне и гармониках ниже 500 Гц, и плоскость у неё низкая.
//
// Числа из журнала ноутбука (03.11.2026): шипение — плоскость 0,599, низкие 0,044,
// звучащих кадров 8; шум микрофона — плоскость 0,26–0,33, низкие 0,45–0,58.
// Речь (образцы из журнала, ok-…m4a): плоскость 0,02–0,20, низкие 0,74–0,90.
func (q SoundQuality) spectrumNoise() bool {
	return q.Voiced >= soundMinVoiced && q.Flatness >= soundFlatness && q.LowBand < soundLowBand
}

// Noisy — шум микрофона: шумоподобный спектр и громкость от soundSilentRMS.
// Тихий шумоподобный звук сюда не попадает — он Silent (тишина), см. ниже.
// Поэтому Noisy и Silent не пересекаются.
func (q SoundQuality) Noisy() bool {
	return q.spectrumNoise() && q.RMS >= soundSilentRMS
}

// Silent — тишина: шумоподобный звук на низком уровне, то есть шипение, а не
// речь. Такой кусок молча пропускается: в облако не уходит и ошибки не даёт.
//
// Три условия сразу. Шумоподобный спектр и низкий общий RMS — это шипение. Третье
// условие — пик кадра ниже soundEventRMS (0,02): громкое событие, даже с плоским
// спектром (шёпот, шипящее, всплеск), тишиной не бывает. Числа: шипение из журнала
// (RMS 0,001, плоскость 0,599, низкие 0,044, звучащие кадры чуть выше 0,008) —
// тишина; ясное слово — пик выше 0,02 — не тишина.
//
// Речеподобное — всё, что не шумоподобно, — в тишину не попадает, даже тихое.
// Громкий шумоподобный звук — Noisy (испорченный вход), если RMS от 0,004.
func (q SoundQuality) Silent() bool {
	return q.spectrumNoise() && q.RMS < soundSilentRMS && q.Peak < soundEventRMS
}

// AssessSound осматривает несжатый звук: громкость, плоскость спектра, доля
// низких частот и доля клиппинга. Зовётся до сжатия — по звуку в памяти.
func AssessSound(pcm []byte) SoundQuality {
	samples := len(pcm) / 2
	if samples == 0 {
		return SoundQuality{}
	}
	var quality SoundQuality
	var energy float64
	clipped := 0
	for i := 0; i+1 < len(pcm); i += 2 {
		value := float64(int16(binary.LittleEndian.Uint16(pcm[i:]))) / 32768
		energy += value * value
		if value >= soundClipLevel || value <= -soundClipLevel {
			clipped++
		}
	}
	quality.RMS = math.Sqrt(energy / float64(samples))
	quality.Clipping = float64(clipped) / float64(samples)
	if samples < soundFrame {
		return quality
	}
	plan := soundSpectrum()
	frame := make([]float64, soundFrame)
	real, imaginary := make([]float64, soundFrame), make([]float64, soundFrame)
	magnitude := make([]float64, soundFrame/2-1)
	flatnesses := make([]float64, 0, samples/soundHop)
	lowBands := make([]float64, 0, samples/soundHop)
	for start := 0; start+soundFrame <= samples; start += soundHop {
		var frameEnergy float64
		for i := 0; i < soundFrame; i++ {
			value := float64(int16(binary.LittleEndian.Uint16(pcm[(start+i)*2:]))) / 32768
			frame[i] = value
			frameEnergy += value * value
		}
		if level := math.Sqrt(frameEnergy / soundFrame); level > quality.Peak {
			quality.Peak = level
		}
		if math.Sqrt(frameEnergy/soundFrame) <= soundActiveRMS {
			continue
		}
		quality.Voiced++
		plan.magnitudes(frame, real, imaginary, magnitude)
		flatnesses = append(flatnesses, spectralFlatness(magnitude))
		lowBands = append(lowBands, lowBandShare(magnitude))
	}
	quality.Flatness = medianOf(flatnesses)
	quality.LowBand = medianOf(lowBands)
	return quality
}

// spectralFlatness — отношение среднего геометрического к среднему
// арифметическому по полосам. У ровного шума близко к единице, у речи мало:
// энергия собрана в нескольких гармониках.
func spectralFlatness(magnitude []float64) float64 {
	if len(magnitude) == 0 {
		return 0
	}
	var sumLog, sum float64
	for _, value := range magnitude {
		if value < 1e-12 {
			value = 1e-12
		}
		sumLog += math.Log(value)
		sum += value
	}
	if sum <= 0 {
		return 0
	}
	return math.Exp(sumLog/float64(len(magnitude))) / (sum / float64(len(magnitude)))
}

// lowBandShare — какая доля энергии лежит ниже soundLowBandEdge.
func lowBandShare(magnitude []float64) float64 {
	bin := sampleRate / soundFrame
	edge := int(soundLowBandEdge) / bin
	if edge < 1 {
		edge = 1
	}
	if edge > len(magnitude) {
		edge = len(magnitude)
	}
	var low, all float64
	for index, value := range magnitude {
		power := value * value
		all += power
		if index < edge {
			low += power
		}
	}
	if all <= 0 {
		return 0
	}
	return low / all
}

// medianOf — срединное значение. Сортируется копия: порядок входа нужен
// вызывающему, а медиана — единственное, что здесь важно.
func medianOf(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return sorted[len(sorted)/2]
}

// spectrumPlan — быстрое преобразование Фурье для одного размера кадра.
//
// Своё, а не из библиотеки: нужен ровно один размер и ровно амплитуды, а
// тянуть ради этого зависимость в службу, которая должна собираться без CGO,
// незачем.
type spectrumPlan struct {
	size    int
	window  []float64
	cos     []float64
	sin     []float64
	reverse []int
}

var soundPlan = newSpectrumPlan(soundFrame)

func soundSpectrum() *spectrumPlan { return soundPlan }

func newSpectrumPlan(size int) *spectrumPlan {
	plan := &spectrumPlan{
		size:    size,
		window:  make([]float64, size),
		cos:     make([]float64, size/2),
		sin:     make([]float64, size/2),
		reverse: make([]int, size),
	}
	for index := range plan.window {
		plan.window[index] = 0.5 * (1 - math.Cos(2*math.Pi*float64(index)/float64(size-1)))
	}
	for index := 0; index < size/2; index++ {
		angle := -2 * math.Pi * float64(index) / float64(size)
		plan.cos[index], plan.sin[index] = math.Cos(angle), math.Sin(angle)
	}
	bits := 0
	for 1<<bits < size {
		bits++
	}
	for index := range plan.reverse {
		value, reversed := index, 0
		for bit := 0; bit < bits; bit++ {
			reversed = reversed<<1 | value&1
			value >>= 1
		}
		plan.reverse[index] = reversed
	}
	return plan
}

// magnitudes считает амплитуды полос, кроме постоянной составляющей: она не
// звук, а сдвиг нуля, и у шума её быть не должно.
func (p *spectrumPlan) magnitudes(frame, real, imaginary, magnitude []float64) {
	n := p.size
	for index := 0; index < n; index++ {
		source := p.reverse[index]
		real[index] = frame[source] * p.window[source]
		imaginary[index] = 0
	}
	for length := 2; length <= n; length <<= 1 {
		half := length / 2
		step := n / length
		for start := 0; start < n; start += length {
			for k := 0; k < half; k++ {
				cosine, sine := p.cos[k*step], p.sin[k*step]
				left, right := start+k, start+k+half
				realPart := real[right]*cosine - imaginary[right]*sine
				imagPart := real[right]*sine + imaginary[right]*cosine
				real[right], imaginary[right] = real[left]-realPart, imaginary[left]-imagPart
				real[left], imaginary[left] = real[left]+realPart, imaginary[left]+imagPart
			}
		}
	}
	for k := 1; k < n/2; k++ {
		magnitude[k-1] = math.Hypot(real[k], imaginary[k])
	}
}
