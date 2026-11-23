//go:build windows

package mediapause

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Системный управляющий мультимедиа Windows (SMTC): его видят кнопки громкости,
// экран блокировки и расширение «Медиа» браузеров. Классы и номера методов
// сверены с windows.media.control.h из Windows SDK 10.0.22621: объект COM — это
// указатель на таблицу, у IInspectable методы начинаются с шестого.

var (
	combase                   = windows.NewLazySystemDLL("combase.dll")
	procRoInitialize          = combase.NewProc("RoInitialize")
	procRoUninitialize        = combase.NewProc("RoUninitialize")
	procRoGetActivationFactry = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString   = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString   = combase.NewProc("WindowsDeleteString")
	procWindowsGetRawBuffer   = combase.NewProc("WindowsGetStringRawBuffer")
)

var (
	iidManagerStatics = windows.GUID{Data1: 0x2050c4ee, Data2: 0x11a0, Data3: 0x57de, Data4: [8]byte{0xae, 0xd7, 0xc9, 0x7c, 0x70, 0x33, 0x82, 0x45}}
	iidAsyncInfo      = windows.GUID{Data1: 0x00000036, Data2: 0, Data3: 0, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
)

const managerClass = "Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager"

// Номера методов в таблицах.
const (
	vRelease         = 2
	vStaticsRequest  = 6 // IManagerStatics.RequestAsync
	vManagerSessions = 7 // IManager.GetSessions
	vViewGetAt       = 6 // IVectorView.GetAt
	vViewSize        = 7 // IVectorView.get_Size
	vSessionAppID    = 6 // ISession.get_SourceAppUserModelId
	vSessionInfo     = 9 // ISession.GetPlaybackInfo
	vSessionPlay     = 10
	vSessionPause    = 11
	vInfoStatus      = 7 // IPlaybackInfo.get_PlaybackStatus
	vAsyncResults    = 8 // IAsyncOperation<T>.GetResults
	vAsyncInfoStatus = 7 // IAsyncInfo.get_Status
	vAsyncInfoCode   = 8 // IAsyncInfo.get_ErrorCode
	vQueryInterface  = 0
)

const (
	statusPlaying = 4
	statusPaused  = 5
	asyncStarted  = 0
	asyncDone     = 1
	rpcChangedMod = 0x80010106
)

// Supported — есть ли на этой системе чем управлять плеерами.
func Supported() bool { return true }

// NewDefault — бэкенд SMTC. Менеджер сессий поднимается лениво и
// запоминается: первый вызов дороже остальных.
func NewDefault() (Backend, error) { return &smtc{}, nil }

type com struct{ p unsafe.Pointer }

func (o com) isNil() bool { return o.p == nil }

func (o com) fn(index int) uintptr { return (*(**[64]uintptr)(o.p))[index] }

func hr(r uintptr) error {
	if int32(r) < 0 {
		return fmt.Errorf("hresult 0x%08x", uint32(r))
	}
	return nil
}

// call0 — метод без доводов.
func (o com) call0(index int) uintptr {
	r, _, _ := syscall.SyscallN(o.fn(index), uintptr(o.p))
	return r
}

// callOut — метод с одним выходным указателем.
func (o com) callOut(index int, out unsafe.Pointer) uintptr {
	r, _, _ := syscall.SyscallN(o.fn(index), uintptr(o.p), uintptr(out))
	return r
}

// callIndexOut — метод с числом и выходным указателем (GetAt).
func (o com) callIndexOut(index int, n uint32, out unsafe.Pointer) uintptr {
	r, _, _ := syscall.SyscallN(o.fn(index), uintptr(o.p), uintptr(n), uintptr(out))
	return r
}

func (o com) release() {
	if o.p != nil {
		o.call0(vRelease)
	}
}

func (o com) queryInterface(iid *windows.GUID) (com, error) {
	var out unsafe.Pointer
	r, _, _ := syscall.SyscallN(o.fn(vQueryInterface), uintptr(o.p), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	return com{out}, hr(r)
}

// waitAsync ждёт конца асинхронной операции WinRT опросом: подписка на
// завершение требует делегата COM, а опрос по миллисекунде стоит дешевле.
func waitAsync(ctx context.Context, op com) error {
	info, err := op.queryInterface(&iidAsyncInfo)
	if err != nil {
		return err
	}
	defer info.release()
	for {
		var status int32
		if err := hr(info.callOut(vAsyncInfoStatus, unsafe.Pointer(&status))); err != nil {
			return err
		}
		switch status {
		case asyncStarted:
		case asyncDone:
			return nil
		default:
			var code int32
			_ = info.callOut(vAsyncInfoCode, unsafe.Pointer(&code))
			return fmt.Errorf("операция завершилась со статусом %d, код 0x%08x", status, uint32(code))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

func hstring(s string) (uintptr, error) {
	u := utf16.Encode([]rune(s))
	var h uintptr
	r, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), uintptr(unsafe.Pointer(&h)))
	runtime.KeepAlive(u)
	return h, hr(r)
}

func fromHString(h uintptr) string {
	if h == 0 {
		return ""
	}
	var length uint32
	raw, _, _ := procWindowsGetRawBuffer.Call(h, uintptr(unsafe.Pointer(&length)))
	if raw == 0 || length == 0 {
		return ""
	}
	// raw — буфер самой строки HSTRING: он жив, пока жива строка.
	return windows.UTF16ToString(unsafe.Slice((*uint16)(*(*unsafe.Pointer)(unsafe.Pointer(&raw))), int(length)))
}

type smtc struct {
	mu      sync.Mutex
	manager com
}

// onThread гоняет f на закреплённой нити с многопоточной квартирой COM:
// WinRT-объекты SMTC свободнопоточные, поэтому их можно хранить между вызовами.
func (b *smtc) onThread(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r, _, _ := procRoInitialize.Call(1) // RO_INIT_MULTITHREADED
	if r == 0 || r == 1 {
		defer func() { _, _, _ = procRoUninitialize.Call() }()
	} else if uint32(r) != rpcChangedMod {
		return hr(r)
	}
	return f()
}

func (b *smtc) sessionManager(ctx context.Context) (com, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.manager.isNil() {
		return b.manager, nil
	}
	class, err := hstring(managerClass)
	if err != nil {
		return com{}, err
	}
	defer func() { _, _, _ = procWindowsDeleteString.Call(class) }()
	var statics unsafe.Pointer
	r, _, _ := procRoGetActivationFactry.Call(class, uintptr(unsafe.Pointer(&iidManagerStatics)), uintptr(unsafe.Pointer(&statics)))
	if err := hr(r); err != nil {
		return com{}, fmt.Errorf("менеджер сессий: %w", err)
	}
	factory := com{statics}
	defer factory.release()
	var op unsafe.Pointer
	if err := hr(factory.callOut(vStaticsRequest, unsafe.Pointer(&op))); err != nil {
		return com{}, err
	}
	operation := com{op}
	defer operation.release()
	if err := waitAsync(ctx, operation); err != nil {
		return com{}, err
	}
	var manager unsafe.Pointer
	if err := hr(operation.callOut(vAsyncResults, unsafe.Pointer(&manager))); err != nil {
		return com{}, err
	}
	b.manager = com{manager}
	return b.manager, nil
}

func (b *smtc) dropManager() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.manager.release()
	b.manager = com{}
}

func playbackStatus(session com) (int32, error) {
	var info unsafe.Pointer
	if err := hr(session.callOut(vSessionInfo, unsafe.Pointer(&info))); err != nil {
		return 0, err
	}
	playback := com{info}
	defer playback.release()
	var status int32
	if err := hr(playback.callOut(vInfoStatus, unsafe.Pointer(&status))); err != nil {
		return 0, err
	}
	return status, nil
}

func appID(session com) string {
	var h uintptr
	if hr(session.callOut(vSessionAppID, unsafe.Pointer(&h))) != nil {
		return ""
	}
	defer func() { _, _, _ = procWindowsDeleteString.Call(h) }()
	return fromHString(h)
}

func (b *smtc) Playing(ctx context.Context) ([]Target, error) {
	var out []Target
	err := b.onThread(func() error {
		list, err := b.sessions(ctx)
		if err != nil {
			// Менеджер мог умереть вместе с сервисом: один раз пробуем заново.
			b.dropManager()
			if list, err = b.sessions(ctx); err != nil {
				return err
			}
		}
		for _, session := range list {
			status, err := playbackStatus(session)
			if err != nil || status != statusPlaying {
				session.release()
				continue
			}
			name := appID(session)
			out = append(out, Target{ID: name, Name: name, handle: session})
		}
		return nil
	})
	return out, err
}

// sessions возвращает все сессии; каждую вызывающий обязан отпустить.
func (b *smtc) sessions(ctx context.Context) ([]com, error) {
	manager, err := b.sessionManager(ctx)
	if err != nil {
		return nil, err
	}
	var vector unsafe.Pointer
	if err := hr(manager.callOut(vManagerSessions, unsafe.Pointer(&vector))); err != nil {
		return nil, err
	}
	view := com{vector}
	defer view.release()
	var size uint32
	if err := hr(view.callOut(vViewSize, unsafe.Pointer(&size))); err != nil {
		return nil, err
	}
	var out []com
	for i := uint32(0); i < size; i++ {
		var item unsafe.Pointer
		if hr(view.callIndexOut(vViewGetAt, i, unsafe.Pointer(&item))) == nil && item != nil {
			out = append(out, com{item})
		}
	}
	return out, nil
}

var errRefused = errors.New("плеер отказал")

// try зовёт TryPlayAsync/TryPauseAsync и ждёт ответа «получилось».
func try(ctx context.Context, session com, method int) error {
	var op unsafe.Pointer
	if err := hr(session.callOut(method, unsafe.Pointer(&op))); err != nil {
		return err
	}
	operation := com{op}
	defer operation.release()
	if err := waitAsync(ctx, operation); err != nil {
		return err
	}
	var ok uint32
	if err := hr(operation.callOut(vAsyncResults, unsafe.Pointer(&ok))); err != nil {
		return err
	}
	if ok&0xff == 0 {
		return errRefused
	}
	return nil
}

func handleOf(t Target) (com, error) {
	session, ok := t.handle.(com)
	if !ok || session.isNil() {
		return com{}, errors.New("нет сессии")
	}
	return session, nil
}

func (b *smtc) Pause(ctx context.Context, t Target) error {
	session, err := handleOf(t)
	if err != nil {
		return err
	}
	return b.onThread(func() error { return try(ctx, session, vSessionPause) })
}

func (b *smtc) Paused(ctx context.Context, t Target) (bool, error) {
	session, err := handleOf(t)
	if err != nil {
		return false, err
	}
	var paused bool
	err = b.onThread(func() error {
		status, err := playbackStatus(session)
		paused = status == statusPaused
		return err
	})
	return paused, err
}

func (b *smtc) Play(ctx context.Context, t Target) error {
	session, err := handleOf(t)
	if err != nil {
		return err
	}
	return b.onThread(func() error { return try(ctx, session, vSessionPlay) })
}

func (b *smtc) Release(t Target) {
	if session, err := handleOf(t); err == nil {
		session.release()
	}
}

func (b *smtc) Close() { b.dropManager() }
