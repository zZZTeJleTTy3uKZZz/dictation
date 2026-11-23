//go:build !windows

package aacrecover

import (
	"context"
	"os/exec"
)

// commandContext на не-Windows: окна консоли там нет, и прятать нечего.
// Отдельный файл нужен, чтобы пакет собирался и проверялся под Linux — на
// машине разработки и в CI, — не утаскивая за собой windows-зависимости.
func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
