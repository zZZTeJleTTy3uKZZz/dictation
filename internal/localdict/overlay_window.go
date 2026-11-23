package localdict

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/chime"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/overlayart"
)

// OverlayState — что показать на плашке. Stage: recording — идёт запись;
// processing — запись остановлена, текст готовится; inserted и pasted — текст
// в поле (галочка); clipboard — текст остался в буфере обмена (надпись);
// autostop — запись остановлена из-за тишины (нейтральная надпись, не ошибка).
// Любая другая стадия, как и Visible=false, плашку гасит.
type OverlayState struct {
	Visible        bool
	Stage          string
	Seconds, Level float64
}

// Overlay — плашка диктовки внизу экрана. Окно живёт в своей нити: Windows
// требует, чтобы сообщения окна разбирала та нить, что его создала.
type Overlay struct {
	Updates chan OverlayState
	Stop    chan struct{}
	closed  chan struct{}
	window  atomic.Uintptr
	dark    func() bool
	// setAt — когда последний раз позвали Set; log — куда писать замеры плашки.
	// Нужны, чтобы жалобу «галочка появляется поздно» можно было разобрать по
	// цифрам: сколько состояние ждало плашку и сколько кадры рисовались.
	setAt atomic.Int64
	log   atomic.Pointer[func(event, meta string)]
}

// SetLog подключает журнал замеров. Журнал зовётся из нити плашки только на
// смене стадии и на уходе плашки, не на каждом кадре, и обязан быть быстрым.
func (o *Overlay) SetLog(log func(event, meta string)) { o.log.Store(&log) }

func (o *Overlay) note(event, meta string) {
	if log := o.log.Load(); log != nil && *log != nil {
		(*log)(event, meta)
	}
}

type windowClass struct {
	Size, Style                        uint32
	Callback                           uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}
type monitorInfo struct {
	Size          uint32
	Monitor, Work rect
	Flags         uint32
}
type point struct{ X, Y int32 }
type bitmapInfo struct {
	Size                         uint32
	Width, Height                int32
	Planes, BitCount             uint16
	Compression, SizeImage       uint32
	XPelsPerMeter, YPelsPerMeter int32
	ClrUsed, ClrImportant        uint32
	Colors                       [4]byte
}
type trackMouse struct {
	Size, Flags uint32
	Window      uintptr
	HoverTime   uint32
}

func NewOverlay(ctx context.Context) *Overlay {
	return newOverlay(ctx, DarkTheme)
}
func newOverlay(ctx context.Context, dark func() bool) *Overlay {
	overlay := &Overlay{Updates: make(chan OverlayState, 1), Stop: make(chan struct{}, 1), closed: make(chan struct{}), dark: dark}
	go overlay.run(ctx)
	return overlay
}

// Set передаёт плашке новое состояние. Не ждёт: если прежнее ещё не забрано,
// оно заменяется — важно только последнее.
func (o *Overlay) Set(state OverlayState) {
	o.setAt.Store(time.Now().UnixNano())
	select {
	case o.Updates <- state:
	default:
		select {
		case <-o.Updates:
		default:
		}
		select {
		case o.Updates <- state:
		default:
		}
	}
}
func (o *Overlay) Wait() { <-o.closed }

// overlayStage переводит стадию приложения в то, что показывает плашка.
func overlayStage(state OverlayState) overlayart.Stage {
	if !state.Visible {
		return overlayart.Hidden
	}
	switch state.Stage {
	case "recording":
		return overlayart.Recording
	case "processing":
		return overlayart.Processing
	case "inserted", "pasted":
		return overlayart.Done
	case "clipboard":
		return overlayart.Clipboard
	case "autostop":
		return overlayart.Stopped
	}
	return overlayart.Hidden
}

func stageName(stage overlayart.Stage) string {
	switch stage {
	case overlayart.Recording:
		return "recording"
	case overlayart.Processing:
		return "processing"
	case overlayart.Done:
		return "done"
	case overlayart.Clipboard:
		return "clipboard"
	case overlayart.Stopped:
		return "stopped"
	}
	return "hidden"
}

// overlayFont — шрифт плашки. Segoe UI Variable есть в Windows 11: у него
// ровнее цифры и мягче буквы; на Windows 10 — обычный Segoe UI.
func overlayFont() string {
	if _, err := os.Stat(filepath.Join(os.Getenv("WINDIR"), "Fonts", "SegUIVar.ttf")); err == nil {
		return "Segoe UI Variable Display"
	}
	return "Segoe UI"
}

// textSupersample — во сколько раз крупнее рисуются буквы перед уменьшением.
const textSupersample = 4

// gdiText рисует текст системой в маску: белые буквы на чёрном, и яркость
// каждого пикселя — его непрозрачность. Цвет кладёт уже overlayart, поэтому
// одна маска годится для любой темы.
//
// Буквы рисуются вчетверо крупнее и уменьшаются усреднением. Собственное
// сглаживание GDI грубое (несколько ступеней серого и подгонка к пикселям),
// и мелкие цифры выходили угловатыми и жирными; после уменьшения край буквы
// плавный, а толщина — та, что задумана шрифтом.
func gdiText() overlayart.Text {
	face := overlayFont()
	return func(s string, size float64, weight int) overlayart.Mask {
		big := renderText(s, size*textSupersample, weight, face)
		if big.W == 0 {
			return big
		}
		const k = textSupersample
		mask := overlayart.Mask{W: (big.W + k - 1) / k, H: (big.H + k - 1) / k}
		mask.A = make([]uint8, mask.W*mask.H)
		for y := 0; y < mask.H; y++ {
			for x := 0; x < mask.W; x++ {
				sum := 0
				for sy := y * k; sy < min(big.H, y*k+k); sy++ {
					for sx := x * k; sx < min(big.W, x*k+k); sx++ {
						sum += int(big.A[sy*big.W+sx])
					}
				}
				mask.A[y*mask.W+x] = uint8(sum / (k * k))
			}
		}
		return mask
	}
}

// renderText — строка белым по чёрному, как её рисует GDI.
func renderText(s string, size float64, weight int, face string) overlayart.Mask {
	dc, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(0)
	if dc == 0 {
		return overlayart.Mask{}
	}
	defer func() { _, _, _ = gdi32.NewProc("DeleteDC").Call(dc) }()
	// 4 — ANTIALIASED_QUALITY: серое сглаживание без цветных краёв ClearType,
	// которые на прозрачной плашке выглядели бы ореолом.
	font, _, _ := gdi32.NewProc("CreateFontW").Call(uintptr(int32(-math.Round(size))), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 4, 0, uintptr(unsafe.Pointer(wide(face))))
	defer func() { _, _, _ = gdi32.NewProc("DeleteObject").Call(font) }()
	priorFont, _, _ := gdi32.NewProc("SelectObject").Call(dc, font)
	defer func() { _, _, _ = gdi32.NewProc("SelectObject").Call(dc, priorFont) }()
	encoded, err := syscall.UTF16FromString(s)
	if err != nil || len(encoded) < 2 {
		return overlayart.Mask{}
	}
	count := uintptr(len(encoded) - 1)
	var extent struct{ X, Y int32 }
	_, _, _ = gdi32.NewProc("GetTextExtentPoint32W").Call(dc, uintptr(unsafe.Pointer(&encoded[0])), count, uintptr(unsafe.Pointer(&extent)))
	pad := int(math.Ceil(size / 12))
	width, height := int(extent.X)+2*pad, int(extent.Y)
	if width <= 2*pad || height <= 0 {
		return overlayart.Mask{}
	}
	info := bitmapInfo{Size: 40, Width: int32(width), Height: -int32(height), Planes: 1, BitCount: 32}
	var bits unsafe.Pointer
	bitmap, _, _ := gdi32.NewProc("CreateDIBSection").Call(dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		return overlayart.Mask{}
	}
	defer func() { _, _, _ = gdi32.NewProc("DeleteObject").Call(bitmap) }()
	priorBitmap, _, _ := gdi32.NewProc("SelectObject").Call(dc, bitmap)
	defer func() { _, _, _ = gdi32.NewProc("SelectObject").Call(dc, priorBitmap) }()
	_, _, _ = gdi32.NewProc("SetBkMode").Call(dc, 1)
	_, _, _ = gdi32.NewProc("SetTextColor").Call(dc, 0xffffff)
	_, _, _ = gdi32.NewProc("TextOutW").Call(dc, uintptr(pad), 0, uintptr(unsafe.Pointer(&encoded[0])), count)
	_, _, _ = gdi32.NewProc("GdiFlush").Call()
	pixels := unsafe.Slice((*byte)(bits), width*height*4)
	mask := overlayart.Mask{W: width, H: height, A: make([]uint8, width*height)}
	for i := range mask.A {
		mask.A[i] = pixels[i*4+1]
	}
	return mask
}

// Звуки плашки синтезируются один раз и только когда понадобятся. Считать их
// при загрузке пакета значило бы делать это и в надзирателе, который плашку
// никогда не показывает: он грузит тот же пакет, что и служба. Буфер обязан
// жить, пока звук играет: PlaySound с SND_MEMORY читает его на ходу, поэтому
// результат лежит в переменной пакета, а не в стеке вызова.
var (
	soundStart     = sync.OnceValue(chime.Start)
	soundStop      = sync.OnceValue(chime.Stop)
	soundDone      = sync.OnceValue(chime.Done)
	soundClipboard = sync.OnceValue(chime.Clipboard)
	soundSilence   = sync.OnceValue(chime.Silence)
	winmm          = syscall.NewLazyDLL("winmm.dll")
)

// sounds — очередь звуков плашки. Играет их своя нить: PlaySound открывает
// звуковое устройство прямо в вызове, и пока он открывал его в нити окна,
// анимация замирала — владелец слышал и видел это как подтормаживание.
var sounds = make(chan []byte, 4)

// soundLoop играет звуки по очереди. Первым — тишина: устройство открывается
// заранее, и первый настоящий звук не ждёт его.
func soundLoop(ctx context.Context) {
	play := func(wav []byte) {
		// SND_MEMORY | SND_ASYNC | SND_NODEFAULT: из памяти, не дожидаясь
		// конца, без системного звука вместо не сыгравшего.
		_, _, _ = winmm.NewProc("PlaySoundW").Call(uintptr(unsafe.Pointer(&wav[0])), 0, 0x4|0x1|0x2)
	}
	play(soundSilence())
	for {
		select {
		case <-ctx.Done():
			return
		case wav := <-sounds:
			play(wav)
		}
	}
}

// playSound ставит звук в очередь и сразу возвращается.
func playSound(wav []byte) {
	if len(wav) == 0 || os.Getenv("DICTATION_SOUNDS") == "0" {
		return
	}
	select {
	case sounds <- wav:
	default:
	}
}

// stageSound — какой звук сопровождает переход плашки из стадии в стадию.
func stageSound(from, to overlayart.Stage) []byte {
	switch {
	case to == overlayart.Recording && from != overlayart.Recording:
		return soundStart()
	case to == overlayart.Processing && from == overlayart.Recording:
		return soundStop()
	case to == overlayart.Done && from != overlayart.Done:
		return soundDone()
	case to == overlayart.Clipboard && from != overlayart.Clipboard:
		return soundClipboard()
	}
	return nil
}

// layeredSurface — кадр для UpdateLayeredWindow: 32-битный рисунок с
// прозрачностью, в который overlayart пишет пиксели.
type layeredSurface struct {
	dc, bitmap, prior uintptr
	pixels            []byte
	canvas            *overlayart.Canvas
}

func newLayeredSurface(scale float64) *layeredSurface {
	canvas := overlayart.NewCanvas(scale)
	dc, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(0)
	if dc == 0 {
		return nil
	}
	info := bitmapInfo{Size: 40, Width: int32(canvas.W), Height: -int32(canvas.H), Planes: 1, BitCount: 32}
	var bits unsafe.Pointer
	bitmap, _, _ := gdi32.NewProc("CreateDIBSection").Call(dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		_, _, _ = gdi32.NewProc("DeleteDC").Call(dc)
		return nil
	}
	prior, _, _ := gdi32.NewProc("SelectObject").Call(dc, bitmap)
	return &layeredSurface{dc: dc, bitmap: bitmap, prior: prior, pixels: unsafe.Slice((*byte)(bits), canvas.W*canvas.H*4), canvas: canvas}
}

func (s *layeredSurface) close() {
	if s == nil {
		return
	}
	_, _, _ = gdi32.NewProc("SelectObject").Call(s.dc, s.prior)
	_, _, _ = gdi32.NewProc("DeleteObject").Call(s.bitmap)
	_, _, _ = gdi32.NewProc("DeleteDC").Call(s.dc)
}

// present отдаёт кадр окну: Windows сам смешивает его с тем, что под окном.
func (s *layeredSurface) present(window uintptr, at point) {
	s.canvas.BGRA(s.pixels)
	screen, _, _ := user32.NewProc("GetDC").Call(0)
	defer func() { _, _, _ = user32.NewProc("ReleaseDC").Call(0, screen) }()
	size := point{int32(s.canvas.W), int32(s.canvas.H)}
	origin := point{}
	// BLENDFUNCTION: AC_SRC_OVER, флаги 0, общая непрозрачность 255,
	// AC_SRC_ALPHA — прозрачность у каждого пикселя своя.
	blend := [4]byte{0, 0, 255, 1}
	_, _, _ = user32.NewProc("UpdateLayeredWindow").Call(window, screen, uintptr(unsafe.Pointer(&at)), uintptr(unsafe.Pointer(&size)),
		s.dc, uintptr(unsafe.Pointer(&origin)), 0, uintptr(unsafe.Pointer(&blend)), 2)
}

func (o *Overlay) run(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(o.closed)
	// Нить плашки просыпается каждые 16 мс и почти ничего не делает; ей важно
	// проснуться вовремя, даже когда машина занята. THREAD_PRIORITY_ABOVE_NORMAL = 1.
	thread, _, _ := kernel32.NewProc("GetCurrentThread").Call()
	_, _, _ = kernel32.NewProc("SetThreadPriority").Call(thread, 1)
	instance, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)

	go soundLoop(ctx)
	renderer := &overlayart.Renderer{Text: gdiText()}
	scale := 1.0
	animator := &overlayart.Animator{Measure: func(text string, timer bool) float64 { return renderer.Measure(text, timer, scale) }}
	var surface *layeredSurface
	defer func() { surface.close() }()
	var frame overlayart.Frame
	var origin point
	dark := o.dark()
	theme := overlayart.ThemeFor(dark)
	var themeAt time.Time
	base := time.Now()
	clock := func() float64 { return time.Since(base).Seconds() }
	tracking := false

	callback := syscall.NewCallback(func(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
		switch msg {
		case 0x21: // WM_MOUSEACTIVATE: плашка не отбирает фокус у поля.
			return 3
		case 0x200: // WM_MOUSEMOVE
			if !tracking {
				track := trackMouse{Size: uint32(unsafe.Sizeof(trackMouse{})), Flags: 2, Window: hwnd}
				_, _, _ = user32.NewProc("TrackMouseEvent").Call(uintptr(unsafe.Pointer(&track)))
				tracking = true
			}
			x, y := float64(int16(lparam&0xffff))/scale, float64(int16((lparam>>16)&0xffff))/scale
			x0, y0, x1, y1 := overlayart.Panel(frame)
			onPanel := x >= x0-4 && x <= x1+4 && y >= y0-4 && y <= y1+4
			onClose := animator.CloseShown() && overlayart.OnClose(frame, x, y)
			animator.SetHover(onPanel || onClose, onClose)
			return 0
		case 0x2a3: // WM_MOUSELEAVE
			tracking = false
			animator.SetHover(false, false)
			return 0
		case 0x201: // WM_LBUTTONDOWN
			x, y := float64(int16(lparam&0xffff))/scale, float64(int16((lparam>>16)&0xffff))/scale
			if animator.CloseShown() && overlayart.OnClose(frame, x, y) {
				animator.SetHover(false, false)
				select {
				case o.Stop <- struct{}{}:
				default:
				}
			}
			return 0
		}
		result, _, _ := user32.NewProc("DefWindowProcW").Call(hwnd, uintptr(msg), wparam, lparam)
		return result
	})
	class := windowClass{Size: uint32(unsafe.Sizeof(windowClass{})), Callback: callback, Instance: instance, Name: wide("LocalDictationOverlay")}
	if registered, _, _ := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class))); registered == 0 {
		return
	}
	defer func() {
		_, _, _ = user32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(class.Name)), instance)
	}()
	width, height := overlayart.CanvasSize()
	// WS_EX_LAYERED | WS_EX_NOACTIVATE | WS_EX_TOOLWINDOW | WS_EX_TOPMOST, WS_POPUP.
	window, _, _ := user32.NewProc("CreateWindowExW").Call(0x08080088, uintptr(unsafe.Pointer(class.Name)), uintptr(unsafe.Pointer(wide("Dictation overlay"))), 0x80000000, 0, 0, uintptr(width), uintptr(height), 0, 0, instance, 0)
	if window == 0 {
		return
	}
	o.window.Store(window)
	defer func() { _, _, _ = user32.NewProc("DestroyWindow").Call(window) }()

	// place ставит плашку внизу по центру того экрана, где окно человека, и
	// пересоздаёт кадр, если у экрана другой масштаб.
	place := func() {
		foreground, _, _ := user32.NewProc("GetForegroundWindow").Call()
		monitor, _, _ := user32.NewProc("MonitorFromWindow").Call(foreground, 2)
		info := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
		_, _, _ = user32.NewProc("GetMonitorInfoW").Call(monitor, uintptr(unsafe.Pointer(&info)))
		var dpiX, dpiY uint32 = 96, 96
		_, _, _ = syscall.NewLazyDLL("shcore.dll").NewProc("GetDpiForMonitor").Call(monitor, 0, uintptr(unsafe.Pointer(&dpiX)), uintptr(unsafe.Pointer(&dpiY)))
		fresh := float64(dpiX) / 96
		if surface == nil || fresh != scale {
			surface.close()
			scale = fresh
			surface = newLayeredSurface(scale)
		}
		if surface == nil {
			return
		}
		// Низ плашки — в 26 пикселях над панелью задач, как и прежде; поле
		// вокруг плашки (тень, крестик) ниже этой черты не считается.
		bottom := info.Work.Bottom - int32(math.Round((overlayart.Margin+overlayart.PanelHeight+26)*scale))
		origin = point{info.Work.Left + (info.Work.Right-info.Work.Left-int32(surface.canvas.W))/2, bottom}
	}

	ticker := time.NewTicker(16 * time.Millisecond)
	defer ticker.Stop()
	shown := false
	// Замеры кадров за время, пока плашка на экране.
	var frames, late int
	var maxGap, maxPresent time.Duration
	var lastFrame time.Time
	var awaitingFrame overlayart.Stage // стадия, чей первый кадр ещё не показан
	for {
		select {
		case <-ctx.Done():
			return
		case fresh := <-o.Updates:
			stage := overlayStage(fresh)
			now := clock()
			if stage != overlayart.Hidden && (!animator.Visible() || (stage == overlayart.Recording && animator.Stage() != overlayart.Recording)) {
				place()
			}
			from := animator.Stage()
			if !animator.Visible() {
				from = overlayart.Hidden
			}
			playSound(stageSound(from, stage))
			animator.Show(stage, fresh.Seconds, now)
			if stage != from && stage != overlayart.Hidden {
				lag := time.Since(time.Unix(0, o.setAt.Load()))
				o.note("overlay-stage", "stage="+stageName(stage)+" lag_ms="+strconv.FormatInt(lag.Milliseconds(), 10))
				if stage == overlayart.Done || stage == overlayart.Clipboard {
					awaitingFrame = stage
				}
			}
			if stage == overlayart.Recording {
				animator.SetLevel(fresh.Level)
			}
		case <-ticker.C:
			if animator.Visible() && surface != nil {
				if time.Since(themeAt) > 500*time.Millisecond {
					dark = o.dark()
					theme = overlayart.ThemeFor(dark)
					themeAt = time.Now()
				}
				var alive bool
				began := time.Now()
				frame, alive = animator.Step(clock())
				if alive {
					renderer.Render(surface.canvas, frame, theme)
					surface.present(window, origin)
					took := time.Since(began)
					if !lastFrame.IsZero() {
						gap := began.Sub(lastFrame)
						maxGap = max(maxGap, gap)
						if gap > 50*time.Millisecond {
							late++
						}
					}
					lastFrame = began
					maxPresent = max(maxPresent, took)
					frames++
					if awaitingFrame != overlayart.Hidden && frame.Stage == awaitingFrame {
						since := time.Since(time.Unix(0, o.setAt.Load()))
						o.note("overlay-frame", "stage="+stageName(awaitingFrame)+" since_set_ms="+strconv.FormatInt(since.Milliseconds(), 10)+" present_ms="+strconv.FormatInt(took.Milliseconds(), 10))
						awaitingFrame = overlayart.Hidden
					}
					if !shown {
						// 4 — SW_SHOWNOACTIVATE: показать, не забирая фокус.
						_, _, _ = user32.NewProc("ShowWindow").Call(window, 4)
						_, _, _ = user32.NewProc("SetWindowPos").Call(window, ^uintptr(0), 0, 0, 0, 0, 0x13)
						shown = true
					}
				}
			}
			if shown && !animator.Visible() {
				_, _, _ = user32.NewProc("ShowWindow").Call(window, 0)
				shown, tracking = false, false
				o.note("overlay-frames", "n="+strconv.Itoa(frames)+" late50="+strconv.Itoa(late)+" max_gap_ms="+strconv.FormatInt(maxGap.Milliseconds(), 10)+" max_present_ms="+strconv.FormatInt(maxPresent.Milliseconds(), 10))
				frames, late, maxGap, maxPresent, lastFrame, awaitingFrame = 0, 0, 0, 0, time.Time{}, overlayart.Hidden
			}
			var msg message
			for {
				got, _, _ := user32.NewProc("PeekMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 1)
				if got == 0 {
					break
				}
				_, _, _ = user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
				_, _, _ = user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
			}
		}
	}
}
