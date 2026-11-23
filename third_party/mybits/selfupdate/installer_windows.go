//go:build windows

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// createNoWindow — CREATE_NO_WINDOW: окно установщика не показывается.
const createNoWindow = 0x08000000

// checkInstallerSupport — на Windows установщик поддерживается.
func checkInstallerSupport() error { return nil }

// runInstaller запускает установщик скрытым окном и требует код выхода 0 (§5).
func runInstaller(ctx context.Context, path string, a Asset) error {
	prog, args, err := installerCommand(path, a)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return withCode(CodeCancelled, fmt.Errorf("%w: %w", ErrCancelled, cerr))
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return codef(CodeInstallFailed, ErrInstallFailed, "установщик %s: код %d: %s",
			filepath.Base(path), ee.ExitCode(), installOutput(out))
	}
	return withCode(CodeInstallFailed, fmt.Errorf("%w: запуск установщика %s: %w", ErrInstallFailed, filepath.Base(path), err))
}

// installOutput — короткий хвост вывода установщика для сообщения об ошибке.
func installOutput(out []byte) string {
	s := strings.TrimSpace(string(out))
	const limit = 200
	if len(s) <= limit {
		return s
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
