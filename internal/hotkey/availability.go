package hotkey

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Кто держит сочетание.
//
// Windows не говорит, кому отказала в регистрации: RegisterHotKey возвращает
// только «занято». Отличить системное переключение ввода от чужой программы
// важно: первое мы имеем право взять низкоуровневым перехватом, а у
// второй отнимать клавишу силой нельзя.
const (
	// StateFree — сочетание свободно: обычная регистрация пройдёт.
	StateFree = "free"
	// StateIME — занято системным переключением ввода (IME): его можно взять
	// перехватом и вернуть человеку рабочую клавишу.
	StateIME = "ime"
	// StateOther — занято другой программой: перехватывать нельзя.
	StateOther = "other"
)

// Availability — ответ на вопрос «свободно ли сочетание и кто его держит».
type Availability struct {
	State string `json:"state"`
	// Owner — словами для человека: «системное переключение ввода» или
	// «другая программа».
	Owner string `json:"owner,omitempty"`
	// IMEID и IMEName — какая именно запись IME держит клавишу. Нужны журналу
	// и проверкам, человеку показывается Owner.
	IMEID   uint32 `json:"ime_id,omitempty"`
	IMEName string `json:"ime_name,omitempty"`
}

// Только свободное можно зарегистрировать; занятое IME — перехватить;
// занятое другой программой — нельзя ни то, ни другое.
func (a Availability) Free() bool      { return a.State == StateFree }
func (a Availability) ByIME() bool     { return a.State == StateIME }
func (a Availability) ByOther() bool   { return a.State == StateOther }
func (a Availability) Available() bool { return a.State != StateOther }

const (
	// alreadyRegistered — ERROR_HOTKEY_ALREADY_REGISTERED.
	alreadyRegistered = 1409

	// Проверка занимает сочетание на мгновение под собственным номером.
	checkHotkeyID = 0x7142
)

// Идентификаторы IME-сочетаний из imm.h: перебираем их через ImmGetHotKey.
//
// Не все они означают «включить/выключить ввод»: 0x11/0x71 переключают форму,
// 0x12/0x72 — символ. Держит клавишу именно 0x10/0x70 (IME_CHOTKEY_IME_NONIME_
// TOGGLE и IME_THOTKEY_IME_NONIME_TOGGLE) — это и есть системное переключение
// ввода, которое видно на машине владельца (замер 16.11.2026).
const (
	imeCHotkeyNoneimeToggle = 0x10
	imeTHotkeyNoneimeToggle = 0x70
)

// imeHotkeyIDs — весь набор IME_CHOTKEY_*, который умеет отдавать ImmGetHotKey.
var imeHotkeyIDs = []uint32{
	0x10, 0x11, 0x12, 0x30, 0x50, 0x51, 0x52, 0x70, 0x71, 0x72,
}

var (
	user32             = syscall.NewLazyDLL("user32.dll")
	imm32              = syscall.NewLazyDLL("imm32.dll")
	procImmGetHotKey   = imm32.NewProc("ImmGetHotKey")
	procImmSetHotKey   = imm32.NewProc("ImmSetHotKey")
	procImmIsIME       = imm32.NewProc("ImmIsIME")
	procRegisterCheck  = user32.NewProc("RegisterHotKey")
	procUnregister     = user32.NewProc("UnregisterHotKey")
	procForeground     = user32.NewProc("GetForegroundWindow")
	procThreadProcess  = user32.NewProc("GetWindowThreadProcessId")
	procKeyboardLayout = user32.NewProc("GetKeyboardLayout")
)

// CheckCombo — свободно ли сочетание и кто его держит.
//
// Ненадолго занимает сочетание, чтобы спросить систему, и сразу отпускает.
// Вызывать, когда своё сочетание уже отпущено (во время выбора) или когда оно
// не совпадает с проверяемым.
func CheckCombo(combo Combo) (Availability, error) {
	errno := tryRegister(combo)
	if errno == alreadyRegistered {
		if owner, held := imeOwner(combo); held {
			return decideAvailability(combo, errno, owner, true)
		}
	}
	return decideAvailability(combo, errno, Availability{}, false)
}

// decideAvailability — правило «свободно / IME / чужая программа».
//
// Вынесено отдельно от Windows-вызовов: решение проверяется без настоящей
// клавиатуры, а от кода остаётся только ответ системы.
func decideAvailability(combo Combo, errno uint32, ime Availability, imeHeld bool) (Availability, error) {
	switch {
	case errno == 0:
		return Availability{State: StateFree}, nil
	case errno == alreadyRegistered && imeHeld:
		return ime, nil
	case errno == alreadyRegistered:
		// Занято, но не системным переключением ввода: отнимать клавишу у
		// чужой программы нельзя.
		return Availability{State: StateOther, Owner: "другая программа"}, nil
	default:
		return Availability{}, fmt.Errorf("не удалось проверить сочетание %s (код %d)", combo, errno)
	}
}

// Check — то же по тексту сочетания из настроек.
func Check(text string) (Availability, error) {
	combo, err := Parse(text)
	if err != nil {
		return Availability{}, err
	}
	return CheckCombo(combo)
}

// tryRegister занимает сочетание, чтобы узнать, свободно ли оно, и сразу
// отпускает. Возвращает код ошибки Windows: 0 — свободно.
func tryRegister(combo Combo) uint32 {
	ok, _, callErr := procRegisterCheck.Call(0, checkHotkeyID, uintptr(combo.Mods|modNoRepeat), uintptr(combo.Key))
	if ok == 0 {
		return errorCode(callErr)
	}
	_, _, _ = procUnregister.Call(0, checkHotkeyID)
	return 0
}

// errorCode — код ошибки из вызова Windows.
func errorCode(err error) uint32 {
	if errno, ok := err.(syscall.Errno); ok {
		return uint32(errno)
	}
	return 0
}

// imeOwner — держит ли сочетание системное переключение ввода.
//
// Источник истины — ImmGetHotKey: он отдаёт настроенные IME-сочетания и на
// машине без восточной раскладки (проверено 16.11.2026). RegisterHotKey для
// Ctrl+Shift+Space при этом проходит, хотя 0x11/0x71 тоже настроены, поэтому
// одного совпадения мало: клавишу держит либо главное переключение 0x10/0x70,
// либо любой IME-набор при активной IME-раскладке. Иначе 1409 от чужой
// программы легко принять за IME и отнять клавишу силой.
func imeOwner(combo Combo) (Availability, bool) {
	active := activeLayoutIsIME()
	for _, id := range imeHotkeyIDs {
		mods, key, ok := immHotKey(id)
		if !ok || !sameHotkey(mods, key, combo) {
			continue
		}
		if id == imeCHotkeyNoneimeToggle || id == imeTHotkeyNoneimeToggle || active {
			return Availability{
				State:   StateIME,
				Owner:   "системное переключение ввода",
				IMEID:   id,
				IMEName: imeHotkeyName(id),
			}, true
		}
	}
	return Availability{}, false
}

// sameHotkey — совпадает ли запись IME с нашим сочетанием.
//
// В старших битах Key Modifiers лежат служебные признаки IME (0xC000), а
// младшие четыре — обычные MOD_ALT/CONTROL/SHIFT/WIN.
func sameHotkey(storedMods, storedKey uint32, combo Combo) bool {
	return uint32(combo.Key) == storedKey && storedMods&0x000F == combo.Mods&0x000F
}

// immHotKey — одно настроенное IME-сочетание.
func immHotKey(id uint32) (mods, key uint32, ok bool) {
	var layout uintptr
	got, _, _ := procImmGetHotKey.Call(
		uintptr(id),
		uintptr(unsafe.Pointer(&mods)),
		uintptr(unsafe.Pointer(&key)),
		uintptr(unsafe.Pointer(&layout)),
	)
	return mods, key, got != 0
}

// activeLayoutIsIME — активная раскладка человека принадлежит IME.
func activeLayoutIsIME() bool {
	window, _, _ := procForeground.Call()
	if window == 0 {
		return false
	}
	var pid uint32
	thread, _, _ := procThreadProcess.Call(window, uintptr(unsafe.Pointer(&pid)))
	layout, _, _ := procKeyboardLayout.Call(thread)
	if layout == 0 {
		return false
	}
	ok, _, _ := procImmIsIME.Call(layout)
	return ok != 0
}

// imeHotkeyName — имя записи IME, как в imm.h.
func imeHotkeyName(id uint32) string {
	if name, ok := imeHotkeyNames[id]; ok {
		return name
	}
	return fmt.Sprintf("IME hotkey 0x%X", id)
}

var imeHotkeyNames = map[uint32]string{
	0x10: "IME_CHOTKEY_IME_NONIME_TOGGLE",
	0x11: "IME_CHOTKEY_SHAPE_TOGGLE",
	0x12: "IME_CHOTKEY_SYMBOL_TOGGLE",
	0x30: "IME_JHOTKEY_CLOSE_OPEN",
	0x50: "IME_KHOTKEY_SHAPE_TOGGLE",
	0x51: "IME_KHOTKEY_HANJACONVERT",
	0x52: "IME_KHOTKEY_ENGLISH",
	0x70: "IME_THOTKEY_IME_NONIME_TOGGLE",
	0x71: "IME_THOTKEY_SHAPE_TOGGLE",
	0x72: "IME_THOTKEY_SYMBOL_TOGGLE",
}
