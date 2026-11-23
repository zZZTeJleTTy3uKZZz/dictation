package localdict

import (
	"fmt"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// Программу с окном Windows спрашивает о выключении сообщением WM_QUERYENDSESSION,
// и на ответ у неё секунды. Без своего окна диктовку гасят вместе с сеансом как
// обычный процесс: звук страницами Ogg на диске остаётся, но пометки о конце и
// закрытого файла нет, а хвост последней страницы теряется. Ровно это и случилось
// 11.11.2026, когда машину выключали посреди диктовки.
const (
	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
	// Имя класса окна — в журналах и в проверке: по нему видно, что окно есть.
	shutdownWindowClass = "DictationShutdownWatch"
	// ERROR_CLASS_ALREADY_EXISTS: окно выключения одно на процесс, и второй
	// вызов не ошибка.
	errorClassAlreadyExists = 1410
)

var (
	shutdownUser32   = syscall.NewLazyDLL("user32.dll")
	shutdownKernel32 = syscall.NewLazyDLL("kernel32.dll")
	shutdownRegister = shutdownUser32.NewProc("RegisterClassExW")
	shutdownCreate   = shutdownUser32.NewProc("CreateWindowExW")
	shutdownDefault  = shutdownUser32.NewProc("DefWindowProcW")
	shutdownModule   = shutdownKernel32.NewProc("GetModuleHandleW")
)

// wndClassEx — WNDCLASSEXW. Своими полями, а не через чужой пакет: окно одно, и
// зависимость ради него в проекте не нужна.
type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

// shutdownApp — приложение, которому сообщают о выключении. Одно на процесс:
// программ с записью здесь не бывает двух.
var shutdownApp atomic.Pointer[App]

// shutdownWindowProc живёт в глобальной переменной: адрес, отданный Windows,
// обязан оставаться годным до конца работы программы.
var shutdownWindowProc = syscall.NewCallback(shutdownProc)

// shutdownProc — обработчик сообщений окна выключения.
func shutdownProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmQueryEndSession:
		// Отвечаем «да, выключайте» — но сперва закрываем запись: заголовок
		// волны дописывается и файл закрывается, пока Windows ещё ждёт ответа.
		if app := shutdownApp.Load(); app != nil {
			path := app.FlushForShutdown()
			app.sessionLog("shutdown-query", fmt.Sprintf("audio=%s", path))
		}
		return 1
	case wmEndSession:
		// wParam = FALSE: выключение отменили, программа остаётся работать.
		if wParam != 0 {
			if app := shutdownApp.Load(); app != nil {
				app.FlushForShutdown()
				app.sessionLog("shutdown-end", "reason=end-session")
				app.cancel()
			}
		}
		return 0
	}
	// lParam передаётся как есть: при создании окна в нём лежит CREATESTRUCT, и
	// подменять его нулём значит провалить создание (WM_NCCREATE).
	result, _, _ := shutdownDefault.Call(hwnd, message, wParam, lParam)
	return result
}

// WatchShutdown заводит окно, через которое Windows спрашивает диктовку о
// выключении. Звать из нити с циклом сообщений — у лотка это onReady: сообщения
// доходят до окна через DispatchMessage этого цикла.
func WatchShutdown(app *App) error {
	_, err := startShutdownWatch(app)
	return err
}

// startShutdownWatch делает то же, что WatchShutdown, и отдаёт дескриптор окна:
// проверке нужно послать ему сообщение, как это сделала бы Windows.
func startShutdownWatch(app *App) (uintptr, error) {
	shutdownApp.Store(app)
	instance, _, _ := shutdownModule.Call(0)
	class, err := syscall.UTF16PtrFromString(shutdownWindowClass)
	if err != nil {
		return 0, err
	}
	title, err := syscall.UTF16PtrFromString("Диктовка: выключение")
	if err != nil {
		return 0, err
	}
	classEx := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:   shutdownWindowProc,
		Instance:  instance,
		ClassName: class,
	}
	if result, _, callErr := shutdownRegister.Call(uintptr(unsafe.Pointer(&classEx))); result == 0 {
		if errno, ok := callErr.(syscall.Errno); !ok || errno != errorClassAlreadyExists {
			return 0, fmt.Errorf("класс окна выключения не заведён: %w", callErr)
		}
	}
	// Обычное (скрытое) окно верхнего уровня, а не message-only: широковещательные
	// сообщения Windows до message-only окон не доходят вовсе.
	window, _, callErr := shutdownCreate.Call(0,
		uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), 0,
		0, 0, 0, 0, 0, 0, instance, 0)
	if window == 0 {
		return 0, fmt.Errorf("окно выключения не создано: %w", callErr)
	}
	return window, nil
}
