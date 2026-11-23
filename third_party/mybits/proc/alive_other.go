//go:build !windows

package proc

import (
	"errors"
	"os/exec"
	"syscall"
)

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	// EPERM — процесс есть, но чужой: он жив.
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Detach отрывает ребёнка от группы и терминала родителя: он переживёт
// закрытие окна и Ctrl+C. Применяется до Start.
func Detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
