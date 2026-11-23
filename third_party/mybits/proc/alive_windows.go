//go:build windows

package proc

import (
	"os/exec"
	"syscall"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
	detachedProcess                = 0x00000008
	createNewProcessGroup          = 0x00000200
)

func alive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		// ERROR_ACCESS_DENIED — процесс есть, но чужой.
		return err == syscall.ERROR_ACCESS_DENIED
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// Detach отрывает ребёнка от консоли и группы родителя (без окна): он
// переживёт закрытие консоли и Ctrl+C. Применяется до Start.
func Detach(cmd *exec.Cmd) {
	Hide(cmd)
	cmd.SysProcAttr.CreationFlags |= detachedProcess | createNewProcessGroup
}
