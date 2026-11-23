// Package overlayart рисует виджет диктовки: плашку записи, обработки и итога.
//
// Почему не GDI. Прежняя плашка рисовалась RoundRect и LineTo, а скруглялась
// регионом окна (SetWindowRgn). Ни то, ни другое не сглаживает: край региона —
// лесенка из целых пикселей, и на светлом фоне плашка выглядела рваной. Здесь
// каждая фигура считается по расстоянию до своего контура, и пиксель на краю
// получает ровно ту долю цвета, которую фигура в нём занимает. Окно получает
// готовую картинку с прозрачностью (UpdateLayeredWindow), поэтому у плашки
// есть мягкая тень, а крестик может выступать за её край.
//
// Пакет не знает про Windows: он отдаёт кадр в памяти, а текст получает от
// вызывающего готовой маской — шрифты остаются системными. Поэтому весь вид
// плашки проверяется где угодно, вплоть до картинки в тесте.
package overlayart

import (
	"math"
)

// Stage — что показывает плашка.
type Stage int

const (
	// Hidden — плашки нет.
	Hidden Stage = iota
	// Recording — идёт запись: волна по голосу, время, крестик по наведению.
	Recording
	// Processing — запись остановлена, текст ещё готовится.
	Processing
	// Done — текст вставлен: галочка.
	Done
	// Clipboard — вставить не удалось, текст в буфере обмена: так и написано.
	Clipboard
	// Stopped — запись остановила сама пауза без голоса. Не ошибка: нейтральная
	// надпись, без красного и без «провала».
	Stopped
)

// Размеры в логических пикселях (96 DPI); масштаб применяет Render.
const (
	// Margin — поле вокруг плашки: в нём живут тень и выступающий крестик.
	Margin = 16.0
	// PanelHeight — высота плашки. Плашка — «пилюля»: радиус равен половине.
	PanelHeight = 32.0
	// RecordingWidth — самая узкая плашка записи: три столбика и одна цифра.
	// Шире она становится вместе с числом секунд.
	RecordingWidth = 52.0
	// DoneWidth — ширина плашки с галочкой.
	DoneWidth = 44.0
	// MaxPanelWidth — шире плашка не бывает; по ней считается размер окна.
	MaxPanelWidth = 200.0
	// CloseRadius — радиус крестика.
	CloseRadius = 8.0

	waveBars = 3
	waveBar  = 3.4
	waveGap  = 2.6
	// padding — поле внутри плашки слева и справа; waveToTimer — от волны до
	// цифр: ровно столько, чтобы они не слипались.
	padding     = 12.0
	waveToTimer = 7.0
	timerSize   = 12.5
	noteSize    = 12.5

	// ClipboardText — что написано, когда текст остался в буфере обмена.
	ClipboardText = "В буфере обмена"
	// StoppedText — что написано, когда запись остановлена из-за тишины.
	StoppedText = "Остановлено: нет голоса"
)

// waveSpan — ширина волны целиком.
const waveSpan = waveBars*waveBar + (waveBars-1)*waveGap

// CanvasSize — размер окна в логических пикселях.
func CanvasSize() (width, height float64) {
	return 2*Margin + MaxPanelWidth, 2*Margin + PanelHeight
}

// Color — цвет с непрозрачностью, компоненты от 0 до 1, не умноженные на альфу.
type Color struct{ R, G, B, A float64 }

func hex(rgb uint32, alpha float64) Color {
	return Color{float64(rgb>>16&0xff) / 255, float64(rgb>>8&0xff) / 255, float64(rgb&0xff) / 255, alpha}
}

func mix(a, b Color, t float64) Color {
	t = clamp(t, 0, 1)
	return Color{a.R + (b.R-a.R)*t, a.G + (b.G-a.G)*t, a.B + (b.B-a.B)*t, a.A + (b.A-a.A)*t}
}

// Theme — цвета плашки под тему Windows. WaveFrom и WaveTo — края градиента
// волны: от голубого к мятному.
type Theme struct {
	Surface, Border, Shadow, Accent, Text, Muted, Success, Danger Color
	WaveFrom, WaveTo                                              Color
}

// ThemeFor — цвета тёмной или светлой темы. Фиолетовый — тот же, что у
// прежней плашки, чтобы виджет остался узнаваемым.
func ThemeFor(dark bool) Theme {
	if dark {
		return Theme{
			Surface:  hex(0x1b1a21, .97),
			Border:   hex(0xffffff, .07),
			Shadow:   hex(0x000000, .42),
			Accent:   hex(0xcdb3ff, 1),
			Text:     hex(0xf6f3ff, 1),
			Muted:    hex(0xa49bb3, 1),
			Success:  hex(0x7fe0b4, 1),
			Danger:   hex(0xf2555a, 1),
			WaveFrom: hex(0x8cc8ff, 1),
			WaveTo:   hex(0x8ee8c4, 1),
		}
	}
	return Theme{
		Surface:  hex(0xfcfbff, .98),
		Border:   hex(0x2a1a55, .08),
		Shadow:   hex(0x241a40, .20),
		Accent:   hex(0x7b58c9, 1),
		Text:     hex(0x2b2538, 1),
		Muted:    hex(0x8a8199, 1),
		Success:  hex(0x1f9d6a, 1),
		Danger:   hex(0xe5484d, 1),
		WaveFrom: hex(0x4c9cf0, 1),
		WaveTo:   hex(0x2fb98d, 1),
	}
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// easeOut — быстрое начало и мягкая остановка.
func easeOut(t float64) float64 {
	t = clamp(t, 0, 1)
	return 1 - math.Pow(1-t, 3)
}

// easeOutBack — с лёгким перелётом в конце: так плашка «встаёт на место».
func easeOutBack(t float64) float64 {
	t = clamp(t, 0, 1)
	const c1 = 1.2
	return 1 + (c1+1)*math.Pow(t-1, 3) + c1*math.Pow(t-1, 2)
}

// easeIn — медленное начало: так уходят.
func easeIn(t float64) float64 {
	t = clamp(t, 0, 1)
	return t * t * t
}

// easeInOut — для движений, у которых видны и начало, и конец.
func easeInOut(t float64) float64 {
	t = clamp(t, 0, 1)
	if t < .5 {
		return 4 * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 3)/2
}
