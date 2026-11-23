package localdict

import (
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Уведомление Windows о том, что случилось без человека.
//
// Своё окно для этого не заводится: плашка диктовки уже создаёт настоящее окно
// в сеансе человека, и значок в лотке на время уведомления вешается на него.
// Всплывающая подсказка (NIF_INFO) — тот же путь, которым Windows показывает
// «скопировано», и он не отбирает фокус у того окна, где человек работает.
//
// Значок в лотке после уведомления убирается: диктовке нечего делать в лотке
// постоянно, а второй значок рядом со значком MeetDigest человек прочитал бы
// как поломку.

var (
	shell32 = syscall.NewLazyDLL("shell32.dll")
	// notifySeq разводит уведомления по номеру: два подряд не должны заменять
	// друг друга, пока первое ещё на экране.
	notifySeq atomic.Uint32
)

const (
	notifyAdd    = 0
	notifyDelete = 2

	notifyFlagIcon  = 0x02
	notifyFlagTip   = 0x04
	notifyFlagState = 0x08
	notifyFlagInfo  = 0x10
	// notifyHidden — значок не показывать: нужно только само уведомление.
	notifyHidden    = 0x01
	notifyInfoInfo  = 0x01
	notifyIconInfo  = 32516 // IDI_INFORMATION
	notifyTimeoutMS = 10000
	notifyLife      = 15 * time.Second
)

// notifyIconData — NOTIFYICONDATAW. Поля идут в том же порядке, что в
// объявлении Windows, включая хвост с GUID и значком подсказки: размер
// структуры проверяет сама Windows, и укороченную не примут.
type notifyIconData struct {
	Size            uint32
	Window          uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Timeout         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            [16]byte
	BalloonIcon     uintptr
}

// balloon показывает системное уведомление поверх чужих окон, не забирая фокус.
// window — окно, к которому временно привязывается значок; 0 — показывать
// некуда (плашка ещё не поднялась), и уведомление пропускается.
func balloon(window uintptr, title, text string) {
	if window == 0 || text == "" {
		return
	}
	icon, _, _ := user32.NewProc("LoadIconW").Call(0, uintptr(notifyIconInfo))
	data := notifyIconData{
		Size:      uint32(unsafe.Sizeof(notifyIconData{})),
		Window:    window,
		ID:        notifySeq.Add(1),
		Flags:     notifyFlagInfo | notifyFlagIcon | notifyFlagTip | notifyFlagState,
		Icon:      icon,
		State:     notifyHidden,
		StateMask: notifyHidden,
		Timeout:   notifyTimeoutMS,
		InfoFlags: notifyInfoInfo,
	}
	putUTF16(data.Tip[:], "Диктовка")
	putUTF16(data.InfoTitle[:], title)
	putUTF16(data.Info[:], text)
	added, _, _ := shell32.NewProc("Shell_NotifyIconW").Call(notifyAdd, uintptr(unsafe.Pointer(&data)))
	if added == 0 {
		return
	}
	time.AfterFunc(notifyLife, func() {
		_, _, _ = shell32.NewProc("Shell_NotifyIconW").Call(notifyDelete, uintptr(unsafe.Pointer(&data)))
	})
}

// putUTF16 кладёт строку в буфер Windows с завершающим нулём. Длинную строку
// обрезает: без нуля Windows прочитает за буфером.
func putUTF16(buffer []uint16, text string) {
	if len(buffer) == 0 {
		return
	}
	encoded, err := syscall.UTF16FromString(text)
	if err != nil {
		return
	}
	if len(encoded) > len(buffer) {
		encoded = encoded[:len(buffer)]
		encoded[len(buffer)-1] = 0
	}
	copy(buffer, encoded)
}
