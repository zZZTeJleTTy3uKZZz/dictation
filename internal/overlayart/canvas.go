package overlayart

import (
	"image"
	"image/color"
	"math"
)

// Canvas — кадр в устройственных пикселях, цвета умножены на альфу.
type Canvas struct {
	W, H int
	// Scale — устройственных пикселей в логическом.
	Scale float64
	pix   []float64
	// zoom — увеличение вокруг (zx, zy) в логических пикселях: им плашка
	// появляется и уходит. 1 — без увеличения.
	zoom, zx, zy float64
}

// setZoom задаёт увеличение всего, что рисуется дальше, вокруг точки (x, y).
func (c *Canvas) setZoom(zoom, x, y float64) { c.zoom, c.zx, c.zy = zoom, x, y }

func (c *Canvas) z() float64 {
	if c.zoom <= 0 {
		return 1
	}
	return c.zoom
}

// NewCanvas — прозрачный кадр размером с окно при данном масштабе.
func NewCanvas(scale float64) *Canvas {
	w, h := CanvasSize()
	c := &Canvas{W: int(math.Ceil(w * scale)), H: int(math.Ceil(h * scale)), Scale: scale}
	c.pix = make([]float64, c.W*c.H*4)
	return c
}

// Clear делает кадр прозрачным.
func (c *Canvas) Clear() {
	for i := range c.pix {
		c.pix[i] = 0
	}
}

// fill кладёт цвет с покрытием cover (0..1) в каждый пиксель прямоугольника,
// заданного в логических координатах. cover получает логические координаты
// середины пикселя — до увеличения: фигуры не знают, что их увеличили.
func (c *Canvas) fill(x0, y0, x1, y1 float64, col Color, alpha float64, cover func(x, y float64) float64) {
	if alpha <= 0 || col.A <= 0 {
		return
	}
	z := c.z()
	x0, x1 = c.zx+(x0-c.zx)*z, c.zx+(x1-c.zx)*z
	y0, y1 = c.zy+(y0-c.zy)*z, c.zy+(y1-c.zy)*z
	px0 := max(0, int(math.Floor(x0*c.Scale)))
	py0 := max(0, int(math.Floor(y0*c.Scale)))
	px1 := min(c.W, int(math.Ceil(x1*c.Scale)))
	py1 := min(c.H, int(math.Ceil(y1*c.Scale)))
	a := col.A * alpha
	for py := py0; py < py1; py++ {
		y := c.zy + ((float64(py)+.5)/c.Scale-c.zy)/z
		for px := px0; px < px1; px++ {
			k := cover(c.zx+((float64(px)+.5)/c.Scale-c.zx)/z, y)
			if k <= 0 {
				continue
			}
			c.blend(px, py, col, a*math.Min(k, 1))
		}
	}
}

// fillShaded — то же, но цвет у каждого пикселя свой (градиент волны).
func (c *Canvas) fillShaded(x0, y0, x1, y1 float64, alpha float64, cover func(x, y float64) (float64, Color)) {
	if alpha <= 0 {
		return
	}
	z := c.z()
	x0, x1 = c.zx+(x0-c.zx)*z, c.zx+(x1-c.zx)*z
	y0, y1 = c.zy+(y0-c.zy)*z, c.zy+(y1-c.zy)*z
	px0 := max(0, int(math.Floor(x0*c.Scale)))
	py0 := max(0, int(math.Floor(y0*c.Scale)))
	px1 := min(c.W, int(math.Ceil(x1*c.Scale)))
	py1 := min(c.H, int(math.Ceil(y1*c.Scale)))
	for py := py0; py < py1; py++ {
		y := c.zy + ((float64(py)+.5)/c.Scale-c.zy)/z
		for px := px0; px < px1; px++ {
			k, col := cover(c.zx+((float64(px)+.5)/c.Scale-c.zx)/z, y)
			if k <= 0 {
				continue
			}
			c.blend(px, py, col, col.A*alpha*math.Min(k, 1))
		}
	}
}

func (c *Canvas) blend(px, py int, col Color, a float64) {
	i := (py*c.W + px) * 4
	keep := 1 - a
	c.pix[i] = col.R*a + c.pix[i]*keep
	c.pix[i+1] = col.G*a + c.pix[i+1]*keep
	c.pix[i+2] = col.B*a + c.pix[i+2]*keep
	c.pix[i+3] = a + c.pix[i+3]*keep
}

// coverage — доля пикселя внутри фигуры по расстоянию d до контура (в
// логических пикселях, внутри — отрицательное): полпикселя в каждую сторону
// от контура и дают сглаженный край.
//
// Переход чуть шире пикселя и по плавной кривой, а не по прямой: прямой
// переход в ровно пиксель даёт на мелкой «пилюле» заметные ступеньки там,
// где край идёт почти вдоль строки.
func (c *Canvas) coverage(d float64) float64 {
	t := clamp(.5-d*c.Scale*c.z()/1.15, 0, 1)
	return t * t * (3 - 2*t)
}

// Расстояния до фигур. Внутри — отрицательные.

func roundRectDist(x, y, cx, cy, halfW, halfH, r float64) float64 {
	qx := math.Abs(x-cx) - (halfW - r)
	qy := math.Abs(y-cy) - (halfH - r)
	outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	return outside + math.Min(math.Max(qx, qy), 0) - r
}

func segmentDist(x, y, ax, ay, bx, by, r float64) float64 {
	px, py := x-ax, y-ay
	dx, dy := bx-ax, by-ay
	length := dx*dx + dy*dy
	t := 0.0
	if length > 0 {
		t = clamp((px*dx+py*dy)/length, 0, 1)
	}
	return math.Hypot(px-dx*t, py-dy*t) - r
}

// Mask — текст, отрисованный системой: непрозрачность каждого пикселя.
// Размер — в устройственных пикселях.
type Mask struct {
	W, H int
	A    []uint8
}

// Text отдаёт маску строки шрифтом размера size (в устройственных пикселях)
// и толщины weight (400 — обычный, 600 — полужирный).
type Text func(s string, size float64, weight int) Mask

// drawMask кладёт маску левым верхним углом в логическую точку (x, y). Без
// увеличения — пиксель в пиксель (так буквы чётче всего), с увеличением —
// с выборкой между пикселями маски.
func (c *Canvas) drawMask(m Mask, x, y float64, col Color, alpha float64) {
	a := col.A * alpha
	if a <= 0 || m.W == 0 {
		return
	}
	z := c.z()
	if math.Abs(z-1) < 1e-3 {
		dx, dy := int(math.Round(x*c.Scale)), int(math.Round(y*c.Scale))
		for my := 0; my < m.H; my++ {
			py := dy + my
			if py < 0 || py >= c.H {
				continue
			}
			for mx := 0; mx < m.W; mx++ {
				px := dx + mx
				if px < 0 || px >= c.W {
					continue
				}
				if k := m.A[my*m.W+mx]; k != 0 {
					c.blend(px, py, col, a*float64(k)/255)
				}
			}
		}
		return
	}
	// Углы маски после увеличения, в устройственных пикселях.
	left := (c.zx + (x-c.zx)*z) * c.Scale
	top := (c.zy + (y-c.zy)*z) * c.Scale
	px0, py0 := max(0, int(math.Floor(left))), max(0, int(math.Floor(top)))
	px1 := min(c.W, int(math.Ceil(left+float64(m.W)*z)))
	py1 := min(c.H, int(math.Ceil(top+float64(m.H)*z)))
	sample := func(mx, my int) float64 {
		if mx < 0 || my < 0 || mx >= m.W || my >= m.H {
			return 0
		}
		return float64(m.A[my*m.W+mx]) / 255
	}
	for py := py0; py < py1; py++ {
		sy := (float64(py)+.5-top)/z - .5
		y0 := int(math.Floor(sy))
		fy := sy - float64(y0)
		for px := px0; px < px1; px++ {
			sx := (float64(px)+.5-left)/z - .5
			x0 := int(math.Floor(sx))
			fx := sx - float64(x0)
			k := (sample(x0, y0)*(1-fx)+sample(x0+1, y0)*fx)*(1-fy) + (sample(x0, y0+1)*(1-fx)+sample(x0+1, y0+1)*fx)*fy
			if k > 0 {
				c.blend(px, py, col, a*k)
			}
		}
	}
}

// BGRA пишет кадр в dst как 32-битные пиксели BGRA с умноженной альфой —
// ровно то, что ждёт UpdateLayeredWindow. dst должен вмещать W*H*4 байт.
func (c *Canvas) BGRA(dst []byte) {
	for i := 0; i < c.W*c.H; i++ {
		s := c.pix[i*4 : i*4+4]
		dst[i*4] = byte(clamp(s[2], 0, 1)*255 + .5)
		dst[i*4+1] = byte(clamp(s[1], 0, 1)*255 + .5)
		dst[i*4+2] = byte(clamp(s[0], 0, 1)*255 + .5)
		dst[i*4+3] = byte(clamp(s[3], 0, 1)*255 + .5)
	}
}

// Image — кадр поверх фона bg: для просмотра глазами и для тестов.
func (c *Canvas) Image(bg color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, c.W, c.H))
	br, bgc, bb := float64(bg.R)/255, float64(bg.G)/255, float64(bg.B)/255
	for i := 0; i < c.W*c.H; i++ {
		s := c.pix[i*4 : i*4+4]
		keep := 1 - s[3]
		img.Pix[i*4] = byte(clamp(s[0]+br*keep, 0, 1)*255 + .5)
		img.Pix[i*4+1] = byte(clamp(s[1]+bgc*keep, 0, 1)*255 + .5)
		img.Pix[i*4+2] = byte(clamp(s[2]+bb*keep, 0, 1)*255 + .5)
		img.Pix[i*4+3] = 255
	}
	return img
}

// Alpha — непрозрачность пикселя: по ней видно, куда окно ловит мышь.
func (c *Canvas) Alpha(px, py int) float64 {
	if px < 0 || py < 0 || px >= c.W || py >= c.H {
		return 0
	}
	return c.pix[(py*c.W+px)*4+3]
}
