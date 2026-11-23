package localdict

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hotkey"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
)

// Освобождение системного Ctrl+Space.
//
// Ctrl+Space у части людей занят системным переключением ввода (IME). Windows
// не отдаёт занятое сочетание, и единственный штатный способ — убрать его из
// настроек IME: ImmSetHotKey и запись HKCU\Control Panel\Input Method\Hot Keys.
// Прежние значения сохраняются, чтобы вернуть их при удалении программы.
//
// В текущем сеансе защищённый ctfmon может ещё держать клавишу: он читает
// настройку при входе. Поэтому после освобождения RegisterHotKey иногда
// удаётся только после выхода и входа в Windows — перезагружать машину не надо.

// CheckHotkey — свободно ли сочетание и кто его держит.
func (a *App) CheckHotkey(text string) (HotkeyStatus, error) {
	combo, err := hotkey.Parse(text)
	if err != nil {
		return HotkeyStatus{}, err
	}
	a.mu.Lock()
	config := a.state.Config
	ours := a.keyCancel != nil && sameHotkeyCombo(config.Hotkey, combo)
	capturing := !a.captureUntil.IsZero()
	released := releasedHotkey(config.ReleasedIME, combo)
	a.mu.Unlock()

	status := HotkeyStatus{Hotkey: combo.String()}
	if ours && !capturing {
		status.State = hotkey.StateFree
		status.Active = true
		status.Note = "действующее сочетание Диктовки"
		return status, nil
	}
	avail, err := hotkey.CheckCombo(combo)
	if err != nil {
		return HotkeyStatus{}, err
	}
	status.State, status.Owner = avail.State, avail.Owner
	status.IMEID, status.IMEName = avail.IMEID, avail.IMEName
	switch {
	case avail.ByIME():
		status.CanRelease = true
		status.Note = fmt.Sprintf("Ctrl+Space занято системным переключением ввода (%s).", avail.IMEName)
	case avail.ByOther() && released:
		status.State = HotkeyStateIMEReleased
		status.Note = "Системное сочетание освобождено. Ctrl+Space заработает после выхода и входа в Windows."
	case avail.ByOther():
		status.Note = "Сочетание занято: другая программа — выберите другое."
	}
	return status, nil
}

// ReleaseHotkey — освободить системное сочетание и забрать его Диктовке.
//
// Зовётся только после согласия человека. Прежние значения IME ложатся в
// config.json, чтобы их можно было вернуть.
func (a *App) ReleaseHotkey(text string) (HotkeyStatus, error) {
	combo, err := hotkey.Parse(text)
	if err != nil {
		return HotkeyStatus{}, err
	}
	status, err := a.CheckHotkey(combo.String())
	if err != nil {
		return HotkeyStatus{}, err
	}
	if status.State != hotkey.StateIME {
		return status, errors.New("сочетание не занято системным переключением ввода")
	}
	saved, err := hotkey.ReleaseIME(combo)
	if err != nil {
		_ = hotkey.RestoreIME(saved)
		return status, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	config := a.state.Config
	config.Hotkey = combo.String()
	for _, entry := range saved {
		if !hasIMEHotkey(config.ReleasedIME, entry.ID) {
			config.ReleasedIME = append(config.ReleasedIME, entry)
		}
	}
	a.state.Config = config
	a.state.HotkeyReleased = true
	if err := a.writeState("config.json", config); err != nil {
		a.state.Trouble = "Сочетание освобождено, но настройку не удалось сохранить: " + err.Error()
		return status, err
	}
	if err := a.setKeys(config); err != nil {
		// Освобождение уже сделано; в этом сеансе клавиша может остаться у
		// защищённого ctfmon до выхода и входа в Windows.
		a.state.Trouble = "Ctrl+Space освобождён. Он заработает после выхода и входа в Windows."
		a.state.HotkeyNote = "освобождено, ждёт входа в Windows"
		return HotkeyStatus{
			Hotkey: combo.String(),
			State:  HotkeyStateIMEReleased,
			Note:   "Освобождено. Ctrl+Space заработает после выхода и входа в Windows.",
		}, nil
	}
	a.state.Trouble = ""
	a.state.HotkeyNote = ""
	return HotkeyStatus{
		Hotkey: combo.String(),
		State:  hotkey.StateFree,
		Active: true,
		Note:   "Ctrl+Space теперь у Диктовки.",
	}, nil
}

// RestoreHotkey — вернуть системные сочетания, которые Диктовка забрала.
func (a *App) RestoreHotkey() (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	saved := a.state.Config.ReleasedIME
	if len(saved) == 0 {
		return false, nil
	}
	if err := hotkey.RestoreIME(saved); err != nil {
		return false, err
	}
	config := a.state.Config
	config.ReleasedIME = nil
	a.state.Config = config
	a.state.HotkeyReleased = false
	_ = a.writeState("config.json", config)
	if err := a.setKeys(config); err != nil {
		a.state.Trouble = "Системное сочетание возвращено. Выберите другое сочетание для диктовки."
		a.state.HotkeyNote = ""
		return true, nil
	}
	a.state.Trouble = ""
	a.state.HotkeyNote = ""
	return true, nil
}

// noteHotkeyProblem — что сказать человеку, когда сочетание не взялось.
func (a *App) noteHotkeyProblem(config Config) {
	combo, err := hotkey.Parse(config.Hotkey)
	if err != nil {
		return
	}
	avail, err := hotkey.CheckCombo(combo)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.state.Trouble = "Сочетание занято. Выберите другое в настройках."
		return
	}
	switch {
	case avail.ByIME() && releasedHotkey(config.ReleasedIME, combo):
		a.state.Trouble = "Ctrl+Space освобождён. Он заработает после выхода и входа в Windows."
		a.state.HotkeyNote = "освобождено, ждёт входа в Windows"
	case avail.ByIME():
		a.state.Trouble = "Ctrl+Space занято системным переключением ввода. Освободите его в настройках или выберите другое."
		a.state.HotkeyNote = "занято системным переключением ввода"
	case avail.ByOther():
		a.state.Trouble = "Сочетание занято другой программой. Выберите другое в настройках."
	default:
		a.state.Trouble = "Сочетание занято. Выберите другое в настройках."
	}
}

// ReleaseDefaultIMEHotkey — установщику: освободить системное Ctrl+Space и
// записать прежние значения в config.json, не поднимая службу.
func ReleaseDefaultIMEHotkey(dir string) (string, error) {
	config, err := readConfig(dir)
	if err != nil {
		return "", err
	}
	hotkeyText := strings.TrimSpace(config.Hotkey)
	if hotkeyText == "" {
		hotkeyText = defaultHotkey
		config.Hotkey = hotkeyText
	}
	combo, err := hotkey.Parse(hotkeyText)
	if err != nil {
		return "", err
	}
	// Сначала спрашиваем систему, кто держит клавишу: занятое другой программой
	// не трогаем. На ПК владельца 16.11.2026 оказалось, что Ctrl+Space держала
	// сама запущенная Диктовка, а не IME — установщик останавливает её до этого
	// шага, поэтому проверка видит настоящее положение.
	avail, err := hotkey.CheckCombo(combo)
	if err != nil {
		return "", err
	}
	if !avail.ByIME() {
		return fmt.Sprintf("сочетание %s не занято системным переключением ввода: системные настройки не менялись", combo.String()), nil
	}
	saved, err := hotkey.ReleaseIME(combo)
	if err != nil {
		return "", err
	}
	for _, entry := range saved {
		if !hasIMEHotkey(config.ReleasedIME, entry.ID) {
			config.ReleasedIME = append(config.ReleasedIME, entry)
		}
	}
	if err := writeJSON(filepath.Join(dir, "config.json"), config); err != nil {
		return "", err
	}
	return fmt.Sprintf("системное сочетание %s освобождено; оно заработает после выхода и входа в Windows", combo.String()), nil
}

// RestoreSavedIMEHotkeys — установщику: вернуть системные сочетания при удалении.
func RestoreSavedIMEHotkeys(dir string) (string, error) {
	config, err := readConfig(dir)
	if err != nil {
		return "", err
	}
	if len(config.ReleasedIME) == 0 {
		return "системные сочетания не были изменены", nil
	}
	if err := hotkey.RestoreIME(config.ReleasedIME); err != nil {
		return "", err
	}
	config.ReleasedIME = nil
	if err := writeJSON(filepath.Join(dir, "config.json"), config); err != nil {
		return "", err
	}
	return "системное сочетание возвращено", nil
}

// readConfig — config.json как есть; испорченный файл даёт умолчания, как и
// запуск службы: установщик не должен падать из-за него.
func readConfig(dir string) (Config, error) {
	made := Config{Hotkey: defaultHotkey, IdleSeconds: 300, PauseMedia: true, AudioKeepDays: defaultAudioKeepDays}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return made, nil
	}
	if err := json.Unmarshal(raw, &made); err != nil {
		return Config{Hotkey: defaultHotkey, IdleSeconds: 300, PauseMedia: true, AudioKeepDays: defaultAudioKeepDays}, nil
	}
	// Тот же перенос прежнего умолчания, что и при запуске службы.
	made.Hotkey = prefs.MigrateHotkey(made.Hotkey)
	return made, nil
}

// releasedHotkey — это сочетание уже освобождали.
func releasedHotkey(saved []hotkey.IMEHotkey, combo hotkey.Combo) bool {
	for _, entry := range saved {
		if entry.Matches(combo) {
			return true
		}
	}
	return false
}

func hasIMEHotkey(saved []hotkey.IMEHotkey, id uint32) bool {
	for _, entry := range saved {
		if entry.ID == id {
			return true
		}
	}
	return false
}

// sameHotkeyCombo — совпадают ли два написания одного сочетания.
func sameHotkeyCombo(text string, combo hotkey.Combo) bool {
	parsed, err := hotkey.Parse(text)
	return err == nil && parsed.Mods == combo.Mods && parsed.Key == combo.Key
}
