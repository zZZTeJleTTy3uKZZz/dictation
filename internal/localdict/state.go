package localdict

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	autostartValue = "Dictation"
	autostartKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	stateBackupDir = "state-backup"
)

// EnsureAutostart restores the per-user startup entry written by the installer.
// The daemon is a logon program: without this value nothing starts it after a
// restart, and dictation silently stops existing for the user. The entry is
// repaired rather than trusted, because a cleanup tool or a partial uninstall
// can remove it while the program stays installed.
func EnsureAutostart(executable string) (bool, error) {
	// При поставленной службе запись в Run не нужна: служба поднимает
	// сеансовую половину сама, а вторая копия на входе — лишний запуск, который
	// гасит мьютекс. Служба снимает эту запись при установке, и сеансовая
	// половина не должна возвращать её обратно.
	if servicePresent() {
		return false, nil
	}
	want := `"` + executable + `" --supervise`
	key, _, err := registry.CreateKey(registry.CURRENT_USER, autostartKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer func() { _ = key.Close() }()
	if current, _, err := key.GetStringValue(autostartValue); err == nil && strings.EqualFold(strings.TrimSpace(current), want) {
		return false, nil
	}
	if err := key.SetStringValue(autostartValue, want); err != nil {
		return false, err
	}
	return true, nil
}

// queryService — запрос к диспетчеру служб о службе «Диктовка». Переменная —
// шов для тестов: настоящий вызов подменяется ошибкой Windows.
var queryService = probeService

// servicePresent — стоит ли служба «Диктовка». Диспетчер спрашиваем с правом
// только на чтение состояния: у обычного человека прав на большее нет.
func servicePresent() bool {
	return servicePresentAfter(queryService())
}

// servicePresentAfter переводит ответ диспетчера в решение. Службы нет только
// при ERROR_SERVICE_DOES_NOT_EXIST. Любая другая ошибка — отказ доступа,
// пометка на удаление и прочее — значит «не знаем», и службу считаем
// присутствующей (fail-closed): автозапуск в сеансе рядом с живой службой дал
// бы вторую копию, а отсутствие записи при неизвестном состоянии не страшно.
func servicePresentAfter(err error) bool {
	if err == nil {
		return true
	}
	return !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST)
}

// probeService спрашивает диспетчер и возвращает причину отказа Windows как есть.
func probeService() error {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseServiceHandle(manager) }()
	name, err := windows.UTF16PtrFromString("Dictation")
	if err != nil {
		return err
	}
	service, err := windows.OpenService(manager, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	_ = windows.CloseServiceHandle(service)
	return nil
}

// stateFiles are the small preferences worth keeping a second copy of.
// Everything the user configured by hand is small and worth duplicating.
var stateFiles = []string{"config.json", providersFile, "history.json", historyStateFile}

// writeState saves a preference and keeps the copy beside the program. A failure
// to write the backup never fails the user's action.
func (a *App) writeState(name string, value any) error {
	err := writeJSON(filepath.Join(a.Dir, name), value)
	backup := filepath.Join(a.Bin, stateBackupDir)
	if os.MkdirAll(backup, 0700) == nil {
		_ = writeJSON(filepath.Join(backup, name), value)
	}
	return err
}

// restoreState returns preferences to the data directory after it has been
// removed or emptied. %LOCALAPPDATA%\Dictation holds the hotkey, the providers
// with their protected keys and the history; losing that whole folder used to
// mean setting everything up again.
func restoreState(dir, bin string) {
	backup := filepath.Join(bin, stateBackupDir)
	for _, name := range stateFiles {
		target := filepath.Join(dir, name)
		if _, err := os.Stat(target); err == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(backup, name))
		if err != nil {
			continue
		}
		if os.MkdirAll(dir, 0700) != nil {
			return
		}
		_ = os.WriteFile(target, data, 0600)
	}
}
