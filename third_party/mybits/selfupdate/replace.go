package selfupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// renameWait — сколько ждать, пока чужой дескриптор (антивирус, индексатор)
// отпустит файл на Windows. Занятость такого рода — на миллисекунды.
const renameWait = 3 * time.Second

// renameRetry — os.Rename с повтором при занятости файла (только Windows:
// на остальных системах busy всегда ложь, и повтора нет).
func renameRetry(from, to string) error {
	deadline := time.Now().Add(renameWait)
	pause := 10 * time.Millisecond
	for {
		err := os.Rename(from, to)
		if err == nil || !busy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(pause)
		pause = min(pause*2, 250*time.Millisecond)
	}
}

// replace ставит newPath на место exe. rename подменяют проверки; nil —
// настоящий переименователь.
//
// POSIX (windowsStyle=false): один rename поверх — атомарно, запущенный
// процесс продолжает работать со старым inode.
//
// Windows-стиль: запущенный .exe нельзя перезаписать или удалить, но можно
// переименовать. Поэтому exe → exe.old, затем new → exe; если второй шаг
// не удался, exe.old возвращается на место, и программа остаётся прежней.
// exe.old убирает CleanupOld при следующем запуске.
func replace(exe, newPath string, windowsStyle bool, rename func(from, to string) error) error {
	if rename == nil {
		rename = renameRetry
	}
	if !windowsStyle {
		if err := rename(newPath, exe); err != nil {
			return fmt.Errorf("selfupdate: замена %s: %w", exe, err)
		}
		syncDir(filepath.Dir(exe))
		return nil
	}

	old := exe + ".old"
	// Остаток прошлой замены. Не удалось (его ещё держит запущенный старый
	// экземпляр) — не беда сама по себе, но rename поверх тогда откажет, и
	// ошибка придёт оттуда.
	_ = os.Remove(old)
	if err := rename(exe, old); err != nil {
		return fmt.Errorf("selfupdate: отодвинуть %s в %s: %w", exe, old, err)
	}
	if err := rename(newPath, exe); err != nil {
		if rerr := rename(old, exe); rerr != nil {
			return fmt.Errorf("selfupdate: поставить новый %s: %w; откат не удался (%w), прежний бинарник лежит как %s",
				exe, err, rerr, old)
		}
		return fmt.Errorf("selfupdate: поставить новый %s (прежний возвращён): %w", exe, err)
	}
	syncDir(filepath.Dir(exe))
	return nil
}

// CleanupOld убирает «<exe>.old», оставшийся от прошлой замены на Windows.
// Звать в начале main. Нет файла — не ошибка. Пустой executable — путь
// текущей программы. Ошибку (файл ещё держит старый экземпляр) обычно можно
// игнорировать: уборка повторится при следующем запуске.
func CleanupOld(executable string) error {
	if executable == "" {
		exe, err := currentExecutable()
		if err != nil {
			return err
		}
		executable = exe
	}
	if err := os.Remove(executable + ".old"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("selfupdate: уборка %s.old: %w", executable, err)
	}
	return nil
}

// currentExecutable — путь запущенной программы с раскрытыми ссылками:
// заменять нужно файл, а не ссылку на него.
func currentExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("selfupdate: путь программы: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}
