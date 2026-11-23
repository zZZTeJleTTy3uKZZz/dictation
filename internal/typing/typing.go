// Package typing — вставить текст туда, где стоит курсор.
//
// ЗАЧЕМ. Диктовка нужна не ради истории, а ради того, чтобы сказанное оказалось
// в письме, в чате, в поле поиска — там, где человек уже печатает. История —
// приятное следствие, а не цель.
//
// КАК ЭТО РАБОТАЕТ. Windows умеет отправить приложению нажатия клавиш так, будто
// их набрали руками (SendInput). Мы посылаем не коды клавиш, а сами буквы
// (KEYEVENTF_UNICODE) — тогда не важно, какая раскладка включена у человека и
// какой язык у текста. Программа на той стороне не отличает это от печати.
//
// ПОЧЕМУ НЕ ЧЕРЕЗ БУФЕР ОБМЕНА. Вставка через Ctrl+V проще, но затирает то, что
// человек туда положил, — а там могла лежать ссылка или кусок кода, который он
// как раз собирался вставить. Буфер принадлежит человеку, а не программе.
//
// НО ЕСЛИ НАБРАТЬ НЕ ВЫШЛО — БУФЕР. Набор вслепую может не сработать: окно
// закрылось, программа не принимает чужие нажатия. Потерять из-за этого
// наговоренное нельзя, и тогда текст кладут в буфер обмена — это единственный
// способ отдать его человеку, когда печатать некуда. Про то, что текст там, ему
// говорят: иначе он искал бы пропавшую речь.
package typing

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const (
	inputKeyboard = 1
	// keyeventfUnicode — «в поле wScan лежит буква, а не код клавиши».
	keyeventfUnicode = 0x0004
	keyeventfKeyUp   = 0x0002
)

// keyInput — то, что SendInput ждёт на входе.
//
// РАЗМЕР ВАЖНЕЕ ПОЛЕЙ. INPUT — это union, и Windows сверяет его длину с тем,
// что ей передали третьим аргументом. На 64 битах длина ровно 40 байт: 8 на
// заголовок плюс 32 на самую большую часть union (мышиную). Ошибёшься на
// восемь байт — система ответит «неверный параметр» и не нажмёт ничего; именно
// так и вышло с первого раза.
//
//	0  Kind          (4) + выравнивание (4)
//	8  VK (2) Scan (2) Flags (4) Time (4) + выравнивание (4)
//	24 Extra         (8)
//	32 хвост до размера мышиной части (8)
type keyInput struct {
	Kind  uint32
	_     uint32
	VK    uint16
	Scan  uint16
	Flags uint32
	Time  uint32
	_     uint32
	Extra uintptr
	_     uint64
}

// betweenKeys — пауза между буквами.
//
// Ноль означает «вся строка одним пакетом», и для большинства программ это
// правильно. Но некоторые (Telegram, отдельные поля в браузере) теряют буквы,
// когда их присылают быстрее, чем они успевают обработать ввод. Пять
// миллисекунд на букву незаметны человеку и снимают потерю.
const betweenKeys = 5 * time.Millisecond

// Type — напечатать текст в окно, которое сейчас активно.
//
// Перед вызовом убедись, что активно нужное окно: наша плашка не должна
// перехватывать фокус, иначе текст уедет в неё.
func Type(text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	send := user32.NewProc("SendInput")

	// Буквы вне основного диапазона (эмодзи) едут двумя половинами — Windows
	// принимает их как есть, по одной единице UTF-16.
	for _, half := range utf16.Encode([]rune(text)) {
		events := []keyInput{
			{Kind: inputKeyboard, Scan: half, Flags: keyeventfUnicode},
			{Kind: inputKeyboard, Scan: half, Flags: keyeventfUnicode | keyeventfKeyUp},
		}
		sent, _, err := send.Call(
			uintptr(len(events)),
			uintptr(unsafe.Pointer(&events[0])),
			unsafe.Sizeof(events[0]),
		)
		if sent == 0 {
			return fmt.Errorf("текст не набрался: %v", err)
		}
		time.Sleep(betweenKeys)
	}
	return nil
}

// Foreground — окно, которое сейчас активно, и его заголовок.
//
// Нужен, чтобы запомнить, куда печатать: пока человек говорит, он может
// переключиться, и текст обязан уйти туда, где диктовка началась, а не туда, где
// оказался курсор в момент окончания.
func Foreground() (uintptr, string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	foreground := user32.NewProc("GetForegroundWindow")
	getText := user32.NewProc("GetWindowTextW")

	handle, _, _ := foreground.Call()
	if handle == 0 {
		return 0, ""
	}
	buffer := make([]uint16, 512)
	length, _, _ := getText.Call(handle, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if length == 0 {
		return handle, ""
	}
	return handle, syscall.UTF16ToString(buffer[:length])
}

// Alive — жив ли тот, кому собрались печатать.
//
// Печатать в закрывшееся окно некуда: Windows примет нажатия и вернёт успех, а
// текст уйдёт в никуда. Отсюда это видно заранее — до того, как сказанное
// потеряется без следа.
func Alive(handle uintptr) bool {
	if handle == 0 {
		return false
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	alive, _, _ := user32.NewProc("IsWindow").Call(handle)
	return alive != 0
}

// clipboardWrite — единственное место, где текст ложится в буфер обмена: в
// проверках оно подменяется, чтобы не трогать буфер живого человека.
var clipboardWrite = copyToClipboard

// Copy — положить текст в буфер обмена.
func Copy(text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return clipboardWrite(text)
}

// copyToClipboard — запись в буфер обмена Windows.
//
// Открытие буфера повторяется: его держит то одна, то другая программа, и
// единственная неудача здесь означала бы потерянную диктовку.
func copyToClipboard(text string) error {
	user32 := syscall.NewLazyDLL("user32.dll")
	open := user32.NewProc("OpenClipboard")
	for attempt := 0; attempt < 10; attempt++ {
		ok, _, _ := open.Call(0)
		if ok != 0 {
			defer func() { _, _, _ = user32.NewProc("CloseClipboard").Call() }()
			return clipboardOpened(text)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("буфер обмена занят")
}

// clipboardOpened — запись в уже открытый буфер.
func clipboardOpened(text string) error {
	user32 := syscall.NewLazyDLL("user32.dll")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	encoded, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	memory, _, _ := kernel32.NewProc("GlobalAlloc").Call(0x42, uintptr(len(encoded)*2))
	if memory == 0 {
		return errors.New("память под буфер не выделилась")
	}
	pointer, _, _ := kernel32.NewProc("GlobalLock").Call(memory)
	if pointer == 0 {
		_, _, _ = kernel32.NewProc("GlobalFree").Call(memory)
		return errors.New("буфер не заперся")
	}
	_, _, _ = kernel32.NewProc("RtlMoveMemory").Call(pointer, uintptr(unsafe.Pointer(&encoded[0])), uintptr(len(encoded)*2))
	_, _, _ = kernel32.NewProc("GlobalUnlock").Call(memory)
	_, _, _ = user32.NewProc("EmptyClipboard").Call()
	ok, _, _ := user32.NewProc("SetClipboardData").Call(13, memory)
	if ok == 0 {
		_, _, _ = kernel32.NewProc("GlobalFree").Call(memory)
		return errors.New("в буфер не легло")
	}
	return nil
}

// Focus — вернуть окну фокус перед печатью.
//
// Windows не даёт забрать фокус кому попало (иначе окна воровали бы его друг у
// друга), поэтому способ обходной: прицепиться к потоку того окна и попросить
// уже как «свой». Не вышло — печатаем всё равно: чаще всего фокус и так там.
func Focus(handle uintptr) {
	if handle == 0 {
		return
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	setForeground := user32.NewProc("SetForegroundWindow")
	threadOfWindow := user32.NewProc("GetWindowThreadProcessId")
	attach := user32.NewProc("AttachThreadInput")
	currentThread := kernel32.NewProc("GetCurrentThreadId")

	theirs, _, _ := threadOfWindow.Call(handle, 0)
	ours, _, _ := currentThread.Call()
	if theirs == 0 || theirs == ours {
		_, _, _ = setForeground.Call(handle)
		return
	}

	_, _, _ = attach.Call(theirs, ours, 1)
	_, _, _ = setForeground.Call(handle)
	_, _, _ = attach.Call(theirs, ours, 0)
}
