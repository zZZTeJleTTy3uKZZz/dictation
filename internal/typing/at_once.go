package typing

import (
	"fmt"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// TypeAtOnce набирает текст настоящими нажатиями, но одним вызовом: все события
// уходят системе сразу, без сна между знаками.
//
// Отличие от Type только в этом. Type спит между знаками, и на длинной диктовке
// это те же минуты ожидания, что и у набора сообщениями. Здесь ожидания нет:
// система получает пачку событий и разбирает её сама, а программа на той стороне
// видит настоящий ввод — тот же, что от рук человека, — и потому принимает его
// без оговорок, в отличие от сообщений о знаке.
//
// Проверяется опытом: то же поле Chromium, тот же длинный текст, сравнение с
// нынешним набором по времени и по числу отказов.
func TypeAtOnce(text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	user32 := syscall.NewLazyDLL("user32.dll")
	send := user32.NewProc("SendInput")

	// Буквы вне основного диапазона (эмодзи) едут двумя половинами — Windows
	// принимает их как есть, по одной единице UTF-16.
	halves := utf16.Encode([]rune(text))
	events := make([]keyInput, 0, len(halves)*2)
	for _, half := range halves {
		events = append(events,
			keyInput{Kind: inputKeyboard, Scan: half, Flags: keyeventfUnicode},
			keyInput{Kind: inputKeyboard, Scan: half, Flags: keyeventfUnicode | keyeventfKeyUp},
		)
	}
	if len(events) == 0 {
		return nil
	}
	sent, _, err := send.Call(
		uintptr(len(events)),
		uintptr(unsafe.Pointer(&events[0])),
		unsafe.Sizeof(events[0]),
	)
	if sent == 0 {
		return fmt.Errorf("текст не набран: %v", err)
	}
	return nil
}
