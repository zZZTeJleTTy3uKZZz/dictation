package overlayart

import (
	"fmt"
	"math"
)

// Renderer рисует кадры и помнит маски текста: время меняется раз в секунду,
// а кадров — шестьдесят, и просить систему о тех же буквах каждый кадр
// незачем.
type Renderer struct {
	Text  Text
	cache map[maskKey]Mask
}

type maskKey struct {
	s      string
	size   float64
	weight int
}

func (r *Renderer) mask(s string, size float64, weight int) Mask {
	if r.Text == nil {
		return Mask{}
	}
	if r.cache == nil || len(r.cache) > 256 {
		r.cache = map[maskKey]Mask{}
	}
	key := maskKey{s, size, weight}
	if m, ok := r.cache[key]; ok {
		return m
	}
	m := r.Text(s, size, weight)
	r.cache[key] = m
	return m
}

// Шрифт: время — обычной толщины, не полужирным: тонкие цифры спокойнее.
const (
	timerWeight = 400
	noteWeight  = 400
)

// Measure — ширина строки в логических пикселях при масштабе scale; её
// аниматор берёт, чтобы плашка была ровно по содержимому.
func (r *Renderer) Measure(text string, timer bool, scale float64) float64 {
	size, weight := noteSize, noteWeight
	if timer {
		size, weight = timerSize, timerWeight
	}
	m := r.mask(text, size*scale, weight)
	if m.W == 0 {
		return float64(len([]rune(text))) * 6.5
	}
	return float64(m.W) / scale
}

// Panel — где плашка в кадре, в логических пикселях (без увеличения).
func Panel(f Frame) (x0, y0, x1, y1 float64) {
	w, _ := CanvasSize()
	x0 = w/2 - f.Width/2
	y0 = Margin + f.Lift
	return x0, y0, x0 + f.Width, y0 + PanelHeight
}

// CloseCenter — середина крестика: на правом верхнем краю плашки, наполовину
// снаружи, чтобы не спорить с временем за место.
func CloseCenter(f Frame) (x, y float64) {
	_, y0, x1, _ := Panel(f)
	return x1 - 4, y0 + 3
}

// OnClose — попадает ли точка (логические пиксели) в крестик.
func OnClose(f Frame, x, y float64) bool {
	cx, cy := CloseCenter(f)
	return math.Hypot(x-cx, y-cy) <= CloseRadius+2
}

// Render рисует кадр f на чистом холсте c.
func (r *Renderer) Render(c *Canvas, f Frame, th Theme) {
	c.Clear()
	if f.Opacity <= 0 {
		return
	}
	op := f.Opacity
	x0, y0, x1, y1 := Panel(f)
	cx, cy := (x0+x1)/2, (y0+y1)/2
	zoom := f.Zoom
	if zoom <= 0 {
		zoom = 1
	}
	c.setZoom(zoom, cx, cy)
	defer c.setZoom(1, 0, 0)
	hw, hh, radius := (x1-x0)/2, PanelHeight/2, PanelHeight/2

	// Тень: мягкий спад за краем плашки, чуть ниже неё.
	const blur, drop = 10.0, 3.0
	c.fill(x0-blur, y0-blur+drop, x1+blur, y1+blur+drop, th.Shadow, op, func(x, y float64) float64 {
		d := roundRectDist(x, y, cx, cy+drop, hw, hh, radius)
		t := clamp((d+2)/blur, 0, 1)
		return (1 - t) * (1 - t)
	})
	// Плашка и тонкая обводка: обводка отделяет плашку от фона того же цвета.
	c.fill(x0-1, y0-1, x1+1, y1+1, th.Surface, op, func(x, y float64) float64 {
		return c.coverage(roundRectDist(x, y, cx, cy, hw, hh, radius))
	})
	c.fill(x0-1, y0-1, x1+1, y1+1, th.Border, op, func(x, y float64) float64 {
		d := roundRectDist(x, y, cx, cy, hw, hh, radius)
		return c.coverage(math.Abs(d+.5) - .5)
	})

	// Содержимое: прежнее гаснет, нынешнее проявляется.
	in := easeOut(f.StageTime / contentFade)
	if f.Previous != Hidden && in < 1 {
		r.content(c, f, f.Previous, th, op*(1-in))
	}
	r.content(c, f, f.Stage, th, op*in)

	if f.Stage == Recording && f.Hover > .01 {
		r.closeButton(c, f, th, op*f.Hover)
	}
}

func (r *Renderer) content(c *Canvas, f Frame, stage Stage, th Theme, alpha float64) {
	if alpha <= 0 {
		return
	}
	switch stage {
	case Recording:
		r.wave(c, f, th, alpha)
		r.timer(c, f, th, alpha)
	case Processing:
		r.spinner(c, f, th, alpha)
	case Done:
		r.check(c, f, th, alpha)
	case Clipboard:
		r.note(c, f, th, alpha, ClipboardText)
	case Stopped:
		r.note(c, f, th, alpha, StoppedText)
	}
}

// WaveHeight — высота столбика волны в логических пикселях. Столбики растут
// от середины в обе стороны: средний — выше крайних, громче голос — выше все.
func WaveHeight(i int, f Frame) float64 {
	const lowest, highest = 4.0, PanelHeight - 14
	weight := .7
	if i == waveBars/2 {
		weight = 1
	}
	voice := math.Pow(clamp(f.Level*10, 0, 1), .7)
	motion := .55 + .45*math.Abs(math.Sin(f.Clock*(5.1+float64(i)*.9)+float64(i)*1.7))
	breathe := 1.2 * (.5 + .5*math.Sin(f.Clock*2.2+float64(i)*1.3))
	return math.Min(highest, lowest+breathe*(1-voice)+(highest-lowest)*voice*weight*motion)
}

func (r *Renderer) wave(c *Canvas, f Frame, th Theme, alpha float64) {
	x0, y0, _, _ := Panel(f)
	cy := y0 + PanelHeight/2
	start := x0 + padding
	for i := 0; i < waveBars; i++ {
		h := math.Max(WaveHeight(i, f), waveBar)
		x := start + float64(i)*(waveBar+waveGap) + waveBar/2
		top, bottom := cy-h/2+waveBar/2, cy+h/2-waveBar/2
		c.fillShaded(x-waveBar, top-waveBar, x+waveBar, bottom+waveBar, alpha, func(px, py float64) (float64, Color) {
			// Градиент идёт через всю волну, а не по столбику: от голубого
			// слева к мятному справа, и чуть светлее к середине по высоте.
			col := mix(th.WaveFrom, th.WaveTo, (px-start)/waveSpan)
			lift := 1 - math.Abs(py-cy)/(PanelHeight/2)
			col = mix(col, Color{1, 1, 1, col.A}, .12*lift)
			return c.coverage(segmentDist(px, py, x, top, x, bottom, waveBar/2)), col
		})
	}
}

// Timer — время записи так, как его видит человек: до минуты — одни секунды
// («1», «2», «59»), дальше — минуты и секунды («1:05»), с часа — и часы.
func Timer(seconds float64) string {
	s := int(math.Max(seconds, 0))
	switch {
	case s < 60:
		return fmt.Sprintf("%d", s)
	case s < 3600:
		return fmt.Sprintf("%d:%02d", s/60, s%60)
	}
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}

// timer рисует время сразу за волной. Цифры у Segoe UI одной ширины, поэтому
// строка рисуется целиком и не дёргается от секунды к секунде.
func (r *Renderer) timer(c *Canvas, f Frame, th Theme, alpha float64) {
	x0, y0, _, _ := Panel(f)
	m := r.mask(Timer(f.Seconds), timerSize*c.Scale, timerWeight)
	if m.W == 0 {
		return
	}
	col := th.Text
	left := x0 + padding + waveSpan + waveToTimer
	top := y0 + PanelHeight/2 - float64(m.H)/c.Scale/2
	c.drawMask(m, left, top, col, alpha*.92)
}

// spinner — кружок обработки: дуга с градиентом волны крутится, а её длина
// чуть дышит. Хвост дуги прозрачнее головы — глаз читает направление.
func (r *Renderer) spinner(c *Canvas, f Frame, th Theme, alpha float64) {
	x0, y0, x1, _ := Panel(f)
	cx, cy := (x0+x1)/2, y0+PanelHeight/2
	const radius, width = 6.6, 2.2
	head := f.Clock * 2 * math.Pi * 1.15
	sweep := 2 * math.Pi * (.72 + .1*math.Sin(f.StageTime*3.2))
	tail := head - sweep
	// Конец дуги — точка на окружности: от неё считается скругление концов.
	end := func(angle float64) (float64, float64) {
		return cx + radius*math.Cos(angle), cy + radius*math.Sin(angle)
	}
	hx, hy := end(head)
	tx, ty := end(tail)
	reach := radius + width
	c.fillShaded(cx-reach, cy-reach, cx+reach, cy+reach, alpha, func(px, py float64) (float64, Color) {
		dx, dy := px-cx, py-cy
		// Где точка на дуге: 0 — хвост, 1 — голова.
		rel := math.Mod(math.Atan2(dy, dx)-tail, 2*math.Pi)
		if rel < 0 {
			rel += 2 * math.Pi
		}
		var d, along float64
		if rel <= sweep {
			d = math.Abs(math.Hypot(dx, dy)-radius) - width/2
			along = rel / sweep
		} else {
			// За пределами дуги — до ближайшего конца: так концы круглые.
			toHead := math.Hypot(px-hx, py-hy)
			toTail := math.Hypot(px-tx, py-ty)
			d = math.Min(toHead, toTail) - width/2
			if toHead < toTail {
				along = 1
			}
		}
		col := mix(th.WaveFrom, th.WaveTo, along)
		col.A *= .2 + .8*along
		return c.coverage(d), col
	})
}

// check — галочка, которая рисуется штрихом.
func (r *Renderer) check(c *Canvas, f Frame, th Theme, alpha float64) {
	x0, y0, x1, _ := Panel(f)
	cx, cy := (x0+x1)/2, y0+PanelHeight/2
	points := [][2]float64{{cx - 5.6, cy + .4}, {cx - 1.8, cy + 4.2}, {cx + 6, cy - 4.5}}
	progress := easeOut((f.StageTime - checkStart) / checkDraw)
	if progress <= 0 {
		return
	}
	first := math.Hypot(points[1][0]-points[0][0], points[1][1]-points[0][1])
	second := math.Hypot(points[2][0]-points[1][0], points[2][1]-points[1][1])
	drawn := (first + second) * progress
	const width = 2.3
	stroke := func(a, b [2]float64, length float64) {
		full := math.Hypot(b[0]-a[0], b[1]-a[1])
		t := clamp(length/full, 0, 1)
		ex, ey := a[0]+(b[0]-a[0])*t, a[1]+(b[1]-a[1])*t
		c.fillShaded(math.Min(a[0], ex)-width, math.Min(a[1], ey)-width, math.Max(a[0], ex)+width, math.Max(a[1], ey)+width, alpha, func(px, py float64) (float64, Color) {
			col := mix(th.WaveFrom, th.WaveTo, (px-(cx-5.6))/11.6)
			return c.coverage(segmentDist(px, py, a[0], a[1], ex, ey, width/2)), mix(col, th.Success, .35)
		})
	}
	stroke(points[0], points[1], drawn)
	if drawn > first {
		stroke(points[1], points[2], drawn-first)
	}
}

// note — значок буфера и надпись «В буфере обмена».
func (r *Renderer) note(c *Canvas, f Frame, th Theme, alpha float64, text string) {
	x0, y0, _, _ := Panel(f)
	cy := y0 + PanelHeight/2
	// Значок: лист с прищепкой — контур и планка сверху.
	ix, iw, ih := x0+padding, 10.0, 12.0
	icx, icy := ix+iw/2, cy+.7
	ink := mix(th.WaveFrom, th.WaveTo, .5)
	c.fill(ix-2, icy-ih/2-2, ix+iw+2, icy+ih/2+2, ink, alpha, func(px, py float64) float64 {
		d := roundRectDist(px, py, icx, icy, iw/2, ih/2, 2.2)
		return c.coverage(math.Abs(d+.7) - .7)
	})
	c.fill(icx-4, icy-ih/2-2.5, icx+4, icy-ih/2+2.5, ink, alpha, func(px, py float64) float64 {
		return c.coverage(roundRectDist(px, py, icx, icy-ih/2+.2, 2.6, 1.4, 1.1))
	})
	m := r.mask(text, noteSize*c.Scale, noteWeight)
	// Надпись слегка выезжает слева: глаз замечает движение раньше текста.
	slide := (1 - easeOut(f.StageTime/(contentFade*2.5))) * 4
	top := cy - float64(m.H)/c.Scale/2
	c.drawMask(m, ix+iw+8-slide, top, th.Text, alpha*.92)
}

// closeButton — крестик на краю плашки: кружок с тенью и два штриха.
func (r *Renderer) closeButton(c *Canvas, f Frame, th Theme, alpha float64) {
	x, y := CloseCenter(f)
	rr := CloseRadius
	c.fill(x-rr-6, y-rr-5, x+rr+6, y+rr+8, th.Shadow, alpha*.7, func(px, py float64) float64 {
		d := math.Hypot(px-x, py-(y+1.5)) - rr
		t := clamp((d+1)/5, 0, 1)
		return (1 - t) * (1 - t)
	})
	bg := mix(th.Surface, th.Danger, f.CloseHover)
	bg.A = math.Max(th.Surface.A, bg.A)
	c.fill(x-rr-1, y-rr-1, x+rr+1, y+rr+1, bg, alpha, func(px, py float64) float64 {
		return c.coverage(math.Hypot(px-x, py-y) - rr)
	})
	c.fill(x-rr-1, y-rr-1, x+rr+1, y+rr+1, th.Border, alpha*(1-f.CloseHover), func(px, py float64) float64 {
		d := math.Hypot(px-x, py-y) - rr
		return c.coverage(math.Abs(d+.5) - .5)
	})
	ink := mix(th.Muted, hex(0xffffff, 1), f.CloseHover)
	const arm, width = 2.7, 1.4
	for _, sign := range []float64{1, -1} {
		ax, ay, bx, by := x-arm, y-arm*sign, x+arm, y+arm*sign
		c.fill(x-arm-width, y-arm-width, x+arm+width, y+arm+width, ink, alpha, func(px, py float64) float64 {
			return c.coverage(segmentDist(px, py, ax, ay, bx, by, width/2))
		})
	}
}
