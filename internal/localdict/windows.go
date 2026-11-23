package localdict

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/proc"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/dictpaster"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hotkey"
	"golang.org/x/sys/windows/registry"
)

var user32 = syscall.NewLazyDLL("user32.dll")
var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var gdi32 = syscall.NewLazyDLL("gdi32.dll")

const editableScript = `Add-Type -AssemblyName UIAutomationClient; Add-Type -AssemblyName UIAutomationTypes; $e=[System.Windows.Automation.AutomationElement]::FocusedElement; $ok=$false; if($e -and $e.Current.IsEnabled -and -not $e.Current.IsPassword){ $p=$null; if($e.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern,[ref]$p)){ $ok=-not $p.Current.IsReadOnly }; if(-not $ok -and $e.Current.ControlType -eq [System.Windows.Automation.ControlType]::Edit){$ok=$true}; if(-not $ok){$p=$null;if($e.TryGetCurrentPattern([System.Windows.Automation.TextPattern]::Pattern,[ref]$p)){ $ok=$p.GetSelection().Length -gt 0 -and $e.Current.ControlType -eq [System.Windows.Automation.ControlType]::Document }} }; if($ok){'true'}else{'false'}`

func wide(text string) *uint16 { value, _ := syscall.UTF16PtrFromString(text); return value }

type rect struct{ Left, Top, Right, Bottom int32 }
type guiInfo struct {
	Size, Flags                               uint32
	Active, Focus, Capture, Menu, Move, Caret uintptr
	Bounds                                    rect
}
type Target struct {
	Window, Focus uintptr
	Identity      string
}

// RaisePriority возвращает программе обычный приоритет.
//
// Задача планировщика запускает диктовку с приоритетом «ниже обычного» (7 по
// умолчанию), и ровно так она и жила на обеих машинах. Пока машина свободна, это
// незаметно. Но когда другие программы занимают все ядра (на ноутбуке это виртуальная
// машина WSL), нить плашки, которая просыпается каждые 16 мс, ждёт очереди целую
// секунду: замер на ноутбуке под такой нагрузкой дал у «ниже обычного» 106
// опозданий больше 50 мс из 1099 и самое долгое 1065 мс, у обычного приоритета —
// одно опоздание и 54 мс. Плашка при этом застывает: крутится старый кружок,
// хотя текст уже в поле.
//
// Повысить приоритет до обычного можно без прав администратора.
func RaisePriority() bool {
	process, _, _ := kernel32.NewProc("GetCurrentProcess").Call()
	const normalPriorityClass = 0x20
	ok, _, _ := kernel32.NewProc("SetPriorityClass").Call(process, normalPriorityClass)
	return ok != 0
}

// priorityClass — нынешний класс приоритета программы (для проверки).
func priorityClass() uint32 {
	process, _, _ := kernel32.NewProc("GetCurrentProcess").Call()
	class, _, _ := kernel32.NewProc("GetPriorityClass").Call(process)
	return uint32(class)
}

// darkCache помнит тему две секунды: Snapshot спрашивает её на каждом кадре,
// а реестр — это не бесплатно, и тема не меняется по десять раз в секунду.
var darkCache struct {
	mu    sync.Mutex
	at    time.Time
	value bool
}

func DarkTheme() bool {
	darkCache.mu.Lock()
	defer darkCache.mu.Unlock()
	if !darkCache.at.IsZero() && time.Since(darkCache.at) < 2*time.Second {
		return darkCache.value
	}
	darkCache.value = readDarkTheme()
	darkCache.at = time.Now()
	return darkCache.value
}

func readDarkTheme() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer func() { _ = key.Close() }()
	value, _, err := key.GetIntegerValue("AppsUseLightTheme")
	return err == nil && value == 0
}

func sameTarget(target Target) bool {
	now := CaptureTarget()
	// Chromium may recreate its focus HWND while the same editor stays active.
	// Recheck editability below; never reactivate an application behind the user.
	return target.Window != 0 && now.Window == target.Window && (target.Focus == 0 || now.Focus == target.Focus || chromiumWindow(target.Window))
}
func chromiumWindow(hwnd uintptr) bool {
	var name [128]uint16
	_, _, _ = user32.NewProc("GetClassNameW").Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	return strings.HasPrefix(syscall.UTF16ToString(name[:]), "Chrome_WidgetWin_")
}

// targetDescription называет поле, в которое пишет диктовка: класс окна и
// программу. Без этого разбор «почему не пишется» начинается с угадывания: в
// журнале от поля остаётся одно «target=true», а поля с одинаковым поведением
// выглядят одинаково, хотя браузер, Electron и родное окно расходятся с
// помощником по-разному. Текста человека здесь нет — только имена.
func targetDescription(target Target) string {
	if target.Window == 0 {
		return "target=none"
	}
	var name [128]uint16
	_, _, _ = user32.NewProc("GetClassNameW").Call(target.Window, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	className := syscall.UTF16ToString(name[:])
	_, pid, _ := user32.NewProc("GetWindowThreadProcessId").Call(target.Window, 0)
	return fmt.Sprintf("class=%s process=%s", className, processName(uint32(pid)))
}

func processName(pid uint32) string {
	if pid == 0 {
		return "unknown"
	}
	const queryLimitedInformation = 0x1000
	handle, _, _ := kernel32.NewProc("OpenProcess").Call(queryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return "unknown"
	}
	defer kernel32.NewProc("CloseHandle").Call(handle)
	var path [512]uint16
	size := uint32(len(path))
	ok, _, _ := kernel32.NewProc("QueryFullProcessImageNameW").Call(handle, 0,
		uintptr(unsafe.Pointer(&path[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 || size == 0 {
		return "unknown"
	}
	return filepath.Base(syscall.UTF16ToString(path[:size]))
}

func CaptureTarget() Target {
	hwnd, _, _ := user32.NewProc("GetForegroundWindow").Call()
	thread, _, _ := user32.NewProc("GetWindowThreadProcessId").Call(hwnd, 0)
	info := guiInfo{Size: uint32(unsafe.Sizeof(guiInfo{}))}
	_, _, _ = user32.NewProc("GetGUIThreadInfo").Call(thread, uintptr(unsafe.Pointer(&info)))
	return Target{Window: hwnd, Focus: info.Focus}
}

func Editable(ctx context.Context) bool {
	now := CaptureTarget()
	var name [128]uint16
	_, _, _ = user32.NewProc("GetClassNameW").Call(now.Focus, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	className := strings.ToLower(syscall.UTF16ToString(name[:]))
	if className == "edit" || strings.HasPrefix(className, "richedit") || strings.Contains(className, ".edit.") {
		style, _, _ := user32.NewProc("GetWindowLongPtrW").Call(now.Focus, ^uintptr(15))
		return style&0x800 == 0 && style&0x20 == 0
	}
	// UI Automation covers Chromium, Flutter and native text controls without
	// guessing from an application's name. A failed probe means clipboard only.
	probe := os.Getenv("DICTATION_FOCUS_PROBE")
	if probe == "" {
		executable, _ := os.Executable()
		probe = filepath.Join(filepath.Dir(executable), "focus-probe.exe")
	}
	var command *exec.Cmd
	if _, err := os.Stat(probe); err == nil {
		command = exec.CommandContext(ctx, probe)
	} else {
		command = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", editableScript)
	}
	proc.Hide(command) // mybits/proc
	output, err := command.Output()
	return err == nil && strings.TrimSpace(string(output)) == "true"
}

// clipboardWrite is the one place a finished dictation reaches the clipboard.
// Acceptance checks replace it to count writes without touching the real one.
var clipboardWrite = Clipboard

// pasteInto — то же для нажатия Ctrl+V: проверка не должна жать клавиши на
// машине, где она идёт.
var pasteInto = PasteInto

// readIntegrityLevels — шов для проверки честного итога: тест подставляет
// уровни, не открывая чужое окно и не собирая его токен.
var readIntegrityLevels = readIntegrityLevelsWindows

// pasteHelper — вызов узкого помощника вставки. Шов для проверки выбора пути.
var pasteHelper = dictpaster.Request

// targetMorePrivileged — цель выше по целостности, и SendInput от нас до неё
// не дойдёт. Равные уровни — обычный путь: решает сама Windows.
func targetMorePrivileged(target, own uint32) bool { return target > own }

// elevatedTargetRoute — выбор пути вставки для окна выше по целостности.
// Первое значение говорит, что маршрут решён и обычный Ctrl+V запускать не
// надо. Помощник есть не у всех: без него честно остаёмся elevated-target;
// отказ помощника — отдельная причина helper-refused.
func elevatedTargetRoute(window uintptr, target, own uint32, readErr error) (bool, bool, string) {
	if readErr != nil || !targetMorePrivileged(target, own) {
		return false, false, ""
	}
	ok, how := pasteHelper(window)
	if ok {
		return true, true, "sent-by-helper"
	}
	if how == "unavailable" {
		return true, false, "elevated-target"
	}
	return true, false, "helper-refused"
}

func Clipboard(text string) error {
	for attempt := 0; attempt < 10; attempt++ {
		ok, _, _ := user32.NewProc("OpenClipboard").Call(0)
		if ok != 0 {
			defer func() { _, _, _ = user32.NewProc("CloseClipboard").Call() }()
			return clipboardOpened(text)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("clipboard is busy")
}
func clipboardOpened(text string) error {
	encoded, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	memory, _, _ := kernel32.NewProc("GlobalAlloc").Call(0x42, uintptr(len(encoded)*2))
	if memory == 0 {
		return errors.New("clipboard allocation failed")
	}
	pointer, _, _ := kernel32.NewProc("GlobalLock").Call(memory)
	if pointer == 0 {
		_, _, _ = kernel32.NewProc("GlobalFree").Call(memory)
		return errors.New("clipboard lock failed")
	}
	_, _, _ = kernel32.NewProc("RtlMoveMemory").Call(pointer, uintptr(unsafe.Pointer(&encoded[0])), uintptr(len(encoded)*2))
	_, _, _ = kernel32.NewProc("GlobalUnlock").Call(memory)
	_, _, _ = user32.NewProc("EmptyClipboard").Call()
	ok, _, _ := user32.NewProc("SetClipboardData").Call(13, memory)
	if ok == 0 {
		_, _, _ = kernel32.NewProc("GlobalFree").Call(memory)
		return errors.New("clipboard write failed")
	}
	return nil
}

type keyInput struct {
	Kind        uint32
	_           uint32
	VK, Scan    uint16
	Flags, Time uint32
	_           uint32
	Extra       uintptr
	_           uint64
}

func Deliver(text string, target Target) (string, error) {
	if err := Clipboard(text); err != nil {
		return "", err
	}
	if !sameTarget(target) {
		return "clipboard", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !Editable(ctx) || !sameTarget(target) {
		return "clipboard", nil
	}
	for attempt := 0; attempt < 100; attempt++ {
		held := false
		for _, key := range []uintptr{0x10, 0x11, 0x12, 0x5b, 0x5c} {
			state, _, _ := user32.NewProc("GetAsyncKeyState").Call(key)
			held = held || state&0x8000 != 0
		}
		if !held {
			break
		}
		if attempt == 99 {
			return "clipboard", nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !sameTarget(target) {
		return "clipboard", nil
	}
	inputs := []keyInput{{Kind: 1, VK: 0x11}, {Kind: 1, VK: 0x56}, {Kind: 1, VK: 0x56, Flags: 2}, {Kind: 1, VK: 0x11, Flags: 2}}
	count, _, err := user32.NewProc("SendInput").Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(inputs[0]))
	if count != uintptr(len(inputs)) {
		return "clipboard", fmt.Errorf("paste blocked by Windows: %v", err)
	}
	return "inserted", nil
}

// PasteInto нажимает Ctrl+V в окне, где начиналась диктовка, — то же, что
// делал бы человек, дождавшись текста в буфере обмена. Нужна там, где поле
// дерево доступности не видит: в терминале (Windows Terminal, консоль) помощник
// вставки поля не находит, и раньше текст молча оставался в буфере, а человек
// ждал и нажимал Ctrl+V сам.
//
// Вставка — только в то же окно: если человек ушёл в другое, нажатие попало бы
// не туда, и текст остаётся в буфере. Клавиши сочетания должны быть отпущены,
// иначе Ctrl+V сложился бы с ними (Ctrl+Shift+V — другая команда).
func PasteInto(target Target) (bool, string) {
	if target.Window == 0 {
		return false, "no-target"
	}
	// Уровень определяется ПРОЦЕССОМ окна (GetWindowThreadProcessId → его
	// токен), а не вкладкой: все вкладки повышенного Windows Terminal ведут
	// себя как администраторские. Такое окно не примет SendInput от процесса
	// среднего уровня — просим помощника, у которого есть повышенный токен.
	targetLevel, ownLevel, ilErr := readIntegrityLevels(target.Window)
	if handled, ok, how := elevatedTargetRoute(target.Window, targetLevel, ownLevel, ilErr); handled {
		return ok, how
	}
	for attempt := 0; ; attempt++ {
		held := false
		for _, key := range []uintptr{0x10, 0x11, 0x12, 0x5b, 0x5c} {
			state, _, _ := user32.NewProc("GetAsyncKeyState").Call(key)
			held = held || state&0x8000 != 0
		}
		if !held {
			break
		}
		if attempt == 100 {
			return false, "keys-held"
		}
		time.Sleep(10 * time.Millisecond)
	}
	if now := CaptureTarget(); now.Window != target.Window {
		return false, "focus-moved"
	}
	inputs := []keyInput{{Kind: 1, VK: 0x11}, {Kind: 1, VK: 0x56}, {Kind: 1, VK: 0x56, Flags: 2}, {Kind: 1, VK: 0x11, Flags: 2}}
	count, _, _ := user32.NewProc("SendInput").Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(inputs[0]))
	if count != uintptr(len(inputs)) {
		return false, "blocked"
	}
	return true, "sent"
}

func ShowExistingUI() bool {
	hwnd, _, _ := user32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(wide("FLUTTER_RUNNER_WIN32_WINDOW"))), uintptr(unsafe.Pointer(wide("Диктовка"))))
	if hwnd == 0 {
		return false
	}
	_, _, _ = user32.NewProc("ShowWindow").Call(hwnd, 9)
	_, _, _ = user32.NewProc("SetForegroundWindow").Call(hwnd)
	return true
}

// KeyEvent — нажали сочетание.
//
// Режим один: нажал — говоришь — нажал. Отпускание клавиши ничего не значит,
// поэтому событий «отпустили» нет вовсе: удержание убрано из программы.
type KeyEvent struct {
	Target Target
}
type message struct {
	Window         uintptr
	ID             uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

// ListenKeys — слушать сочетание, пока не погасят.
//
// Каждое нажатие — одно событие. Прежде здесь был второй режим, удержание:
// событие «отпустили» приходило, когда клавишу отпускали. Удержание убрано из
// программы, и вместе с ним ушёл опрос GetAsyncKeyState.
func ListenKeys(ctx context.Context, combo hotkey.Combo, events chan<- KeyEvent, ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	register := user32.NewProc("RegisterHotKey")
	unregister := user32.NewProc("UnregisterHotKey")
	peek := user32.NewProc("PeekMessageW")
	ok, _, err := register.Call(0, 17, uintptr(combo.Mods|0x4000), uintptr(combo.Key))
	if ok == 0 {
		ready <- fmt.Errorf("hotkey %s is unavailable: %v", combo.String(), err)
		return
	}
	defer func() { _, _, _ = unregister.Call(0, 17) }()
	ready <- nil
	ticker := time.NewTicker(15 * time.Millisecond)
	defer ticker.Stop()
	emit := func() {
		select {
		case events <- KeyEvent{CaptureTarget()}:
		case <-ctx.Done():
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var msg message
			for {
				got, _, _ := peek.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 1)
				if got == 0 {
					break
				}
				if msg.ID == 0x312 {
					emit()
				}
			}
		}
	}
}

func OpenMicrophoneSettings() {
	command := exec.Command("cmd.exe", "/c", "start", "", "ms-settings:privacy-microphone")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = command.Run()
}
func JSON(value any) []byte { data, _ := json.Marshal(value); return data }
