// Package proc — дочерние процессы без окна консоли Windows (скрыть окно у
// git, gh и прочих), проверка «жив ли процесс» (Alive) и отрыв от консоли и
// группы родителя (Detach). Запускает фоновые команды с единой политикой окон.
//
// Контекст и маршруты stdin/stdout/stderr задаёт вызывающий. По умолчанию
// os/exec направляет отсутствующие потоки в нулевое устройство. Output и
// CombinedOutput сохраняют обычные коды завершения и ошибки os/exec.
package proc

import (
	"context"
	"os/exec"
)

// CommandContext создаёт команду без промежуточной оболочки и без окна
// консоли Windows. Срок выполнения задаётся контекстом вызывающего.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	Hide(cmd)
	return cmd
}
