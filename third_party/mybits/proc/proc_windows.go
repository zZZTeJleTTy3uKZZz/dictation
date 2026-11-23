//go:build windows

package proc

import (
	"os/exec"
	"syscall"
)

// Hide применяется до Start. Настройки наследования и группы сохраняются;
// несовместимый CREATE_NEW_CONSOLE снимается. DETACHED_PROCESS уже исключает
// консоль, остальные флаги вызывающего остаются без изменений.
func Hide(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags &^= 0x00000010
	cmd.SysProcAttr.CreationFlags |= 0x08000000
}
