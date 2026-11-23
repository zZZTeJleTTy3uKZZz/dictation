package overlayart

import "math"

// Сроки анимаций, в секундах. Короткие нарочно: плашка должна ощущаться
// живой, но не задерживать человека ни на миг дольше нужного.
const (
	appearTime     = .32
	fadeTime       = .22
	contentFade    = .12
	checkStart     = .08
	checkDraw      = .24
	doneHold       = .5
	clipboardHold  = 1.6
	widthSharpness = 16.0
	hoverSharpness = 18.0
	levelSharpness = 16.0
)

// Animator — что и как показывает плашка в каждый миг. Окно сообщает ему
// события (смену стадии, наведение мыши, громкость), а на каждом кадре
// забирает Frame. Время — секунды от любой точки отсчёта, лишь бы одной.
type Animator struct {
	// Measure — ширина строки в логических пикселях: timer — цифры времени,
	// иначе надпись. По ней плашка раздвигается ровно под содержимое.
	Measure func(text string, timer bool) float64

	visible  bool
	shownAt  float64
	stage    Stage
	previous Stage
	stageAt  float64
	hideAt   float64 // начало ухода; 0 — не уходит
	last     float64

	seconds  float64
	rawLevel float64
	level    float64
	width    float64

	panelHover, closeHover bool
	hoverAmount, closeAmt  float64
}

// Frame — один кадр плашки.
type Frame struct {
	Stage, Previous Stage
	// StageTime — сколько секунд идёт нынешняя стадия.
	StageTime float64
	// Clock — сколько секунд плашка на экране: от него движется волна.
	Clock   float64
	Seconds float64
	Level   float64
	// Opacity — непрозрачность всей плашки; Lift — насколько она ниже своего
	// места; Zoom — её увеличение (появляется с 0,9, уходит к 0,94).
	Opacity, Lift, Zoom float64
	Width               float64
	// Hover — мышь над плашкой (0..1, плавно); CloseHover — над крестиком.
	Hover, CloseHover float64
}

// Visible — есть ли что показывать.
func (a *Animator) Visible() bool { return a.visible }

// Stage — нынешняя стадия.
func (a *Animator) Stage() Stage { return a.stage }

// Show переводит плашку в стадию. Hidden прячет её с уходом. Повтор той же
// стадии только обновляет время записи.
func (a *Animator) Show(stage Stage, seconds float64, now float64) {
	if stage == Hidden {
		a.Hide(now)
		return
	}
	a.seconds = seconds
	if stage == Recording && (!a.visible || a.stage != Recording) {
		// Новая запись начинается с чистого листа, даже если прежний итог ещё
		// уходит: человек уже говорит снова.
		a.visible, a.shownAt, a.hideAt = true, now, 0
		a.stage, a.previous, a.stageAt = Recording, Hidden, now
		a.width, a.level, a.rawLevel = a.targetWidth(Recording), 0, 0
		a.hoverAmount, a.closeAmt = 0, 0
		a.last = now
		return
	}
	if !a.visible {
		// Итог без записи (запись шла без плашки): показываем его сразу.
		a.visible, a.shownAt, a.hideAt = true, now, 0
		a.width, a.last = a.targetWidth(stage), now
		a.stage, a.previous, a.stageAt = stage, Hidden, now
	}
	if stage == a.stage {
		return
	}
	a.previous, a.stage, a.stageAt = a.stage, stage, now
	a.hideAt = 0
	switch stage {
	case Done:
		a.hideAt = now + checkStart + checkDraw + doneHold
	case Clipboard, Stopped:
		a.hideAt = now + contentFade + clipboardHold
	}
}

// Hide убирает плашку плавно; если она уже уходит, срок не сдвигается.
func (a *Animator) Hide(now float64) {
	if !a.visible {
		return
	}
	if a.hideAt == 0 || a.hideAt > now {
		a.hideAt = now
	}
}

// SetLevel — громкость голоса сейчас, 0..1; плашка сглаживает её сама.
func (a *Animator) SetLevel(level float64) { a.rawLevel = level }

// SetHover — где мышь: над плашкой и над крестиком.
func (a *Animator) SetHover(panel, close bool) { a.panelHover, a.closeHover = panel, close }

// CloseShown — виден ли крестик сейчас настолько, чтобы его можно было нажать.
func (a *Animator) CloseShown() bool {
	return a.visible && a.stage == Recording && a.hoverAmount > .3
}

func (a *Animator) measure(text string, timer bool) float64 {
	if a.Measure != nil {
		return a.Measure(text, timer)
	}
	return float64(len([]rune(text))) * 6.5
}

func (a *Animator) targetWidth(stage Stage) float64 {
	switch stage {
	case Processing, Done:
		// Обработка и итог — один кружок: крутится, потом в нём же галочка.
		return DoneWidth
	case Clipboard:
		return math.Min(MaxPanelWidth, padding+12+8+a.measure(ClipboardText, false)+padding+2)
	case Stopped:
		return math.Min(MaxPanelWidth, padding+12+8+a.measure(StoppedText, false)+padding+2)
	}
	return math.Max(RecordingWidth, padding+waveSpan+waveToTimer+a.measure(Timer(a.seconds), true)+padding)
}

// approach — плавное приближение к цели, не зависящее от частоты кадров.
func approach(value, target, sharpness, dt float64) float64 {
	return target + (value-target)*math.Exp(-sharpness*dt)
}

// Step продвигает анимацию к моменту now и отдаёт кадр. alive=false — плашка
// ушла, окно можно прятать.
func (a *Animator) Step(now float64) (frame Frame, alive bool) {
	if !a.visible {
		return Frame{}, false
	}
	dt := clamp(now-a.last, 0, .1)
	a.last = now
	a.level = approach(a.level, clamp(a.rawLevel, 0, 1), levelSharpness, dt)
	a.width = approach(a.width, a.targetWidth(a.stage), widthSharpness, dt)
	hover := 0.0
	if a.panelHover && a.stage == Recording {
		hover = 1
	}
	a.hoverAmount = approach(a.hoverAmount, hover, hoverSharpness, dt)
	closeHover := 0.0
	if a.closeHover && a.stage == Recording {
		closeHover = 1
	}
	a.closeAmt = approach(a.closeAmt, closeHover, hoverSharpness, dt)

	// Появление: проявляется быстрее, чем встаёт на место, — как переход в
	// интерфейсе, а не как вспышка.
	t := (now - a.shownAt) / appearTime
	opacity := easeOut(t * 1.6)
	zoom := .9 + .1*easeOutBack(t)
	lift := 8 * (1 - easeOut(t))
	if a.hideAt != 0 && now >= a.hideAt {
		out := (now - a.hideAt) / fadeTime
		if out >= 1 {
			a.visible, a.stage, a.hideAt = false, Hidden, 0
			return Frame{}, false
		}
		// Уход: гаснет, чуть уменьшаясь и опускаясь.
		opacity *= 1 - easeIn(out)
		zoom *= 1 - .06*easeInOut(out)
		lift += 3 * easeInOut(out)
	}
	return Frame{
		Stage: a.stage, Previous: a.previous,
		StageTime: now - a.stageAt, Clock: now - a.shownAt,
		Seconds: a.seconds, Level: a.level,
		Opacity: opacity, Lift: lift, Zoom: zoom,
		Width: a.width, Hover: a.hoverAmount, CloseHover: a.closeAmt,
	}, true
}
