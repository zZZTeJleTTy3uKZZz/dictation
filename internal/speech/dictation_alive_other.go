//go:build !windows

package speech

import "syscall"

// dictationAlive — живо ли то, о чём говорят сведения.
//
// Вне Windows службы диктовки с её служебным профилем нет, а номера сведений в
// проверках всегда живые: проверяется разговор со службой, а не эта половина.
// Поэтому здесь та же проверка средствами системы: процесс по номеру есть.
func dictationAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil || syscall.Kill(pid, 0) == syscall.EPERM
}
