// Package hotkey — глобальное сочетание клавиш Windows.
//
// ЗАЧЕМ. Заметку голосом надо начать в тот момент, когда мысль пришла, — не
// найдя окно, не прицелившись мышью в значок. Одно нажатие из любой программы.
//
// КАК ЭТО РАБОТАЕТ. Windows умеет сама отдать сочетание одной программе:
// RegisterHotKey просит систему присылать нам сообщение, когда человек нажал
// именно эту комбинацию. Это не слежка за клавиатурой: никаких других нажатий
// мы не видим и видеть не можем — перехватчик всего ввода (хук) нам не нужен и
// был бы непорядочным.
//
// ПОЧЕМУ ОТДЕЛЬНЫЙ ПОТОК. Сообщения Windows приходят в тот поток, который
// зарегистрировал сочетание, и только пока он разбирает свою очередь. Поэтому
// поток закрепляется за системным (LockOSThread) и разбирает очередь сам.
package hotkey

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Модификаторы Windows (MOD_*).
const (
	modAlt     = 0x0001
	modControl = 0x0002
	modShift   = 0x0004
	modWin     = 0x0008
	// modNoRepeat — не повторять, пока клавишу держат.
	//
	// Без этого зажатая клавиша начинает и тут же останавливает запись десятки
	// раз в секунду, и в истории остаётся мусор из пустых заметок.
	modNoRepeat = 0x4000
)

// wmHotkey — сообщение «нажали зарегистрированное сочетание».
const wmHotkey = 0x0312

// pollEvery — как часто заглядывать в очередь сообщений.
//
// 40 мс: человек этой задержки не замечает, а поток при этом не крутится
// вхолостую на полном ядре.
const pollEvery = 40 * time.Millisecond

// Combo — сочетание клавиш.
type Combo struct {
	// Mods — набор модификаторов Windows.
	Mods uint32
	// Key — код клавиши (virtual-key code).
	Key uint32
	// Text — как человек это написал; храним, чтобы показать в настройках ровно
	// то, что он видел.
	Text string
}

// DefaultCombo — сочетание по умолчанию.
//
// Ctrl+Пробел: два пальца одной руки, и Windows его не занимает — в отличие от
// Ctrl+Shift+любой буквы, которую занимают правка текста и браузеры. У части
// людей Ctrl+Пробел переключает раскладку или IME; тогда сочетание меняют в
// настройках, и запасное там же — Ctrl+Alt+Space.
const DefaultCombo = "Ctrl+Space"

// keys — названия клавиш, которые человек может написать в настройках.
var keys = map[string]uint32{
	"space": 0x20, "enter": 0x0D, "tab": 0x09, "escape": 0x1B, "esc": 0x1B,
	"insert": 0x2D, "delete": 0x2E, "home": 0x24, "end": 0x23,
	"pageup": 0x21, "pagedown": 0x22,
	"up": 0x26, "down": 0x28, "left": 0x25, "right": 0x27,
	"backquote": 0xC0, "minus": 0xBD, "plus": 0xBB,
	"bracketleft": 0xDB, "bracketright": 0xDD, "backslash": 0xDC,
	"semicolon": 0xBA, "quote": 0xDE, "comma": 0xBC, "period": 0xBE, "slash": 0xBF,
	"backspace": 0x08, "capslock": 0x14, "printscreen": 0x2C, "scrolllock": 0x91, "pause": 0x13,
	"intlbackslash": 0xE2,
	"numpad0":       0x60, "numpad1": 0x61, "numpad2": 0x62, "numpad3": 0x63, "numpad4": 0x64,
	"numpad5": 0x65, "numpad6": 0x66, "numpad7": 0x67, "numpad8": 0x68, "numpad9": 0x69,
	"numpadmultiply": 0x6A, "numpadadd": 0x6B, "numpadsubtract": 0x6D, "numpaddecimal": 0x6E, "numpaddivide": 0x6F,
}

// names — обратный перевод: код клавиши в человеческое слово.
var names = map[uint32]string{}

func init() {
	// Пишем первое встреченное имя: у «escape» и «esc» один код, а показать
	// человеку надо одно слово, и пусть это будет длинное — оно понятнее.
	best := map[uint32]string{}
	for name, code := range keys {
		if kept, ok := best[code]; !ok || len(name) > len(kept) {
			best[code] = name
		}
	}
	for code, name := range best {
		names[code] = strings.ToUpper(name[:1]) + name[1:]
	}
}

// Parse — разобрать сочетание из настроек.
//
// Пустая строка и мусор — это не повод работать без горячей клавиши молча:
// возвращаем ошибку, а вызывающий ставит значение по умолчанию и говорит об
// этом в журнале.
func Parse(text string) (Combo, error) {
	parts := strings.Split(text, "+")
	made := Combo{}
	for _, raw := range parts {
		part := strings.ToLower(strings.TrimSpace(raw))
		switch part {
		case "":
			continue
		case "ctrl", "control":
			made.Mods |= modControl
		case "alt":
			made.Mods |= modAlt
		case "shift":
			made.Mods |= modShift
		case "win", "meta", "cmd":
			made.Mods |= modWin
		default:
			code, err := keyCode(part)
			if err != nil {
				return Combo{}, err
			}
			if made.Key != 0 {
				return Combo{}, fmt.Errorf("в сочетании две обычные клавиши: %q", text)
			}
			made.Key = code
		}
	}
	if made.Key == 0 {
		return Combo{}, fmt.Errorf("в сочетании нет клавиши: %q", text)
	}
	if made.Mods == 0 {
		// Сочетание без модификатора забрало бы клавишу у всех программ: нажать
		// «D» в текстовом редакторе стало бы невозможно.
		return Combo{}, fmt.Errorf("нужен хотя бы один модификатор: %q", text)
	}
	made.Text = made.String()
	return made, nil
}

// keyCode — код одной клавиши.
func keyCode(part string) (uint32, error) {
	if code, ok := keys[part]; ok {
		return code, nil
	}
	if len(part) == 1 {
		letter := part[0]
		if letter >= 'a' && letter <= 'z' {
			return uint32(letter - 'a' + 'A'), nil
		}
		if letter >= '0' && letter <= '9' {
			return uint32(letter), nil
		}
	}
	if strings.HasPrefix(part, "f") {
		if number, err := strconv.Atoi(part[1:]); err == nil && number >= 1 && number <= 24 {
			return uint32(0x70 + number - 1), nil // VK_F1 = 0x70
		}
	}
	return 0, fmt.Errorf("непонятная клавиша: %q", part)
}

// String — как сочетание выглядит для человека.
//
// Порядок модификаторов всегда один и тот же, как в самой Windows: иначе
// «Shift+Ctrl+D» и «Ctrl+Shift+D» выглядели бы разными настройками.
func (c Combo) String() string {
	var parts []string
	for _, one := range []struct {
		mask uint32
		name string
	}{
		{modControl, "Ctrl"}, {modAlt, "Alt"}, {modShift, "Shift"}, {modWin, "Win"},
	} {
		if c.Mods&one.mask != 0 {
			parts = append(parts, one.name)
		}
	}
	parts = append(parts, KeyName(c.Key))
	return strings.Join(parts, "+")
}

// KeyName — человеческое имя клавиши.
func KeyName(code uint32) string {
	if name, ok := names[code]; ok {
		return name
	}
	switch {
	case code >= 'A' && code <= 'Z', code >= '0' && code <= '9':
		return string(rune(code))
	case code >= 0x70 && code <= 0x87:
		return "F" + strconv.Itoa(int(code-0x70+1))
	}
	return fmt.Sprintf("клавиша %#x", code)
}

// Suggestions — сочетания на выбор в настройках.
//
// Список, а не свободный ввод: человек не должен угадывать, как писать
// «пробел», и не должен выяснять опытным путём, что его сочетание занято.
func Suggestions() []string {
	return []string{
		"Ctrl+Space",
		// Запасное на случай, когда Ctrl+Пробел у человека переключает
		// раскладку или IME: добавляем Alt и остаёмся на пробеле.
		"Ctrl+Alt+Space",
		"Ctrl+Shift+Space",
		"Ctrl+Shift+D",
		"Ctrl+Alt+D",
		"Ctrl+Shift+F12",
		"Alt+Win+Space",
	}
}

// Listen — слушать сочетание, пока не погасят.
//
// Вызов `pressed` происходит в этом же потоке, поэтому он обязан быть быстрым:
// долгая работа задержит следующее нажатие. Всё тяжёлое — в свою горутину.
func Listen(ctx context.Context, combo Combo, log *slog.Logger, pressed func()) error {
	ready := make(chan error, 1)
	go func() {
		// Сообщения приходят в поток, который зарегистрировал сочетание, —
		// значит поток нельзя отпускать планировщику Go.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		user32 := syscall.NewLazyDLL("user32.dll")
		register := user32.NewProc("RegisterHotKey")
		unregister := user32.NewProc("UnregisterHotKey")
		peek := user32.NewProc("PeekMessageW")

		const id = 1
		ok, _, err := register.Call(0, id, uintptr(combo.Mods|modNoRepeat), uintptr(combo.Key))
		if ok == 0 {
			// Чаще всего это значит «сочетание занято другой программой» —
			// молчать нельзя, человек будет жать и недоумевать.
			ready <- fmt.Errorf("сочетание %s занято другой программой (%v)", combo, err)
			return
		}
		defer func() { _, _, _ = unregister.Call(0, id) }()
		ready <- nil
		log.Info("слушаю сочетание", "сочетание", combo.String())

		type message struct {
			Handle  uintptr
			Message uint32
			WParam  uintptr
			LParam  uintptr
			Time    uint32
			X, Y    int32
		}
		var msg message
		const removeFromQueue = 0x0001

		for {
			if ctx.Err() != nil {
				return
			}
			got, _, _ := peek.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, removeFromQueue)
			if got == 0 {
				time.Sleep(pollEvery)
				continue
			}
			if msg.Message == wmHotkey {
				pressed()
			}
		}
	}()

	select {
	case err := <-ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SortedSuggestions — подсказки по алфавиту (для окон, которым нужен порядок).
func SortedSuggestions() []string {
	found := append([]string(nil), Suggestions()...)
	sort.Strings(found)
	return found
}
