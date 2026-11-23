package hotkey

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// IMEHotkey — одна запись системного переключения ввода.
//
// Хранится затем, чтобы вернуть всё как было при удалении программы или по
// кнопке «Вернуть системное сочетание».
type IMEHotkey struct {
	ID   uint32 `json:"id"`
	Name string `json:"name,omitempty"`
	// Mods и Key — как в реестре IME: старшие биты Mods (0xC000) служебные.
	Mods uint32 `json:"mods"`
	Key  uint32 `json:"key"`
	// Target — значение Target IME как было.
	Target []byte `json:"target,omitempty"`
	// Existed — подраздел реестра был до освобождения. Нет — значение жило
	// только в памяти IME, и при возврате подраздел надо убрать.
	Existed bool `json:"existed"`
}

// Matches — эта запись держит то же сочетание.
func (h IMEHotkey) Matches(combo Combo) bool {
	return sameHotkey(h.Mods, h.Key, combo)
}

// Сочетания, которые держат клавишу у системного переключения ввода. Остальные
// IME-сочетания (форма, символ) не трогаем: они не совпадают с нашим.
var imeToggleIDs = []uint32{imeCHotkeyNoneimeToggle, imeTHotkeyNoneimeToggle}

const imeHotKeysPath = `Control Panel\Input Method\Hot Keys`

// IMEHotkeysFor — какие записи системного переключения держат сочетание.
func IMEHotkeysFor(combo Combo) []IMEHotkey {
	var found []IMEHotkey
	for _, id := range imeToggleIDs {
		entry, ok := imeHotkeyEntry(id)
		if !ok || !sameHotkey(entry.Mods, entry.Key, combo) {
			continue
		}
		found = append(found, entry)
	}
	return found
}

// imeHotkeyEntry — нынешнее значение записи IME и её след в реестре.
func imeHotkeyEntry(id uint32) (IMEHotkey, bool) {
	entry := IMEHotkey{ID: id, Name: imeHotkeyName(id)}
	mods, key, ok := immHotKey(id)
	if ok {
		entry.Mods, entry.Key = mods, key
	}
	if storedMods, storedKey, target, existed := registryHotkey(id); existed {
		entry.Mods, entry.Key, entry.Target, entry.Existed = storedMods, storedKey, target, true
		ok = true
	}
	return entry, ok
}

// ReleaseIME — освободить сочетание у системного переключения ввода.
//
// Освобождение штатное: ImmSetHotKey и та же запись в
// HKCU\Control Panel\Input Method\Hot Keys, поэтому она держится после выхода и
// входа в Windows. В текущем сеансе ctfmon может ещё держать клавишу: он
// защищённый процесс и читает настройку при входе. Возвращает то, что было,
// чтобы вернуть при удалении.
func ReleaseIME(combo Combo) ([]IMEHotkey, error) {
	held := IMEHotkeysFor(combo)
	if len(held) == 0 {
		return nil, fmt.Errorf("сочетание %s не занято системным переключением ввода", combo)
	}
	var saved []IMEHotkey
	for _, entry := range held {
		saved = append(saved, entry)
		if err := clearIMEHotkey(entry.ID); err != nil {
			return saved, err
		}
	}
	return saved, nil
}

// clearIMEHotkey — убрать одну запись IME и её след в реестре.
func clearIMEHotkey(id uint32) error {
	// ImmSetHotKey удаляет и запись в реестре; реестр чистим следом, потому что
	// на машине без действующей IME-раскладки вызов может только записать
	// «пусто», а не удалить подраздел.
	ok, _, callErr := procImmSetHotKey.Call(uintptr(id), 0, 0, 0)
	if err := deleteRegistryHotkey(id); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	if ok == 0 {
		return fmt.Errorf("ImmSetHotKey(0x%X): %v", id, callErr)
	}
	return nil
}

// RestoreIME — вернуть системные сочетания как было.
func RestoreIME(saved []IMEHotkey) error {
	if len(saved) == 0 {
		return nil
	}
	var problems []string
	for _, entry := range saved {
		if entry.Existed {
			if err := writeRegistryHotkey(entry); err != nil {
				problems = append(problems, err.Error())
				continue
			}
		}
		ok, _, callErr := procImmSetHotKey.Call(uintptr(entry.ID), uintptr(entry.Mods), uintptr(entry.Key), 0)
		if ok == 0 {
			problems = append(problems, fmt.Sprintf("ImmSetHotKey(0x%X): %v", entry.ID, callErr))
			continue
		}
		if !entry.Existed {
			// Значение жило только в памяти IME: в реестре его быть не должно.
			if err := deleteRegistryHotkey(entry.ID); err != nil && !errors.Is(err, registry.ErrNotExist) {
				problems = append(problems, err.Error())
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// registryHotkey — что лежит в реестре про запись IME.
func registryHotkey(id uint32) (mods, key uint32, target []byte, existed bool) {
	path := fmt.Sprintf(`%s\%08X`, imeHotKeysPath, id)
	sub, err := registry.OpenKey(registry.CURRENT_USER, path, registry.READ)
	if err != nil {
		return 0, 0, nil, false
	}
	defer sub.Close()
	mods, ok := registryDword(sub, "Key Modifiers")
	if !ok {
		return 0, 0, nil, false
	}
	key, ok = registryDword(sub, "Virtual Key")
	if !ok {
		return 0, 0, nil, false
	}
	target, _, _ = sub.GetBinaryValue("Target IME")
	return mods, key, target, true
}

// registryDword — значение из четырёх байт.
func registryDword(sub registry.Key, name string) (uint32, bool) {
	raw, _, err := sub.GetBinaryValue(name)
	if err != nil || len(raw) < 4 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(raw), true
}

// writeRegistryHotkey — вернуть запись IME в реестр.
func writeRegistryHotkey(entry IMEHotkey) error {
	path := fmt.Sprintf(`%s\%08X`, imeHotKeysPath, entry.ID)
	sub, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.WRITE)
	if err != nil {
		return err
	}
	defer sub.Close()
	if err := sub.SetBinaryValue("Key Modifiers", dword(entry.Mods)); err != nil {
		return err
	}
	if err := sub.SetBinaryValue("Virtual Key", dword(entry.Key)); err != nil {
		return err
	}
	target := entry.Target
	if len(target) < 4 {
		target = []byte{0, 0, 0, 0}
	}
	return sub.SetBinaryValue("Target IME", target)
}

// deleteRegistryHotkey — убрать запись IME из реестра.
func deleteRegistryHotkey(id uint32) error {
	return registry.DeleteKey(registry.CURRENT_USER, fmt.Sprintf(`%s\%08X`, imeHotKeysPath, id))
}

func dword(value uint32) []byte {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, value)
	return raw
}
