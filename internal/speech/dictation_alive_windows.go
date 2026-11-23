//go:build windows

package speech

import "syscall"

// dictationAlive — живо ли то, о чём говорят сведения.
//
// Отказ в доступе — это не «мёртв»: служба, поднятая от имени системы, чужому
// процессу именно так и отвечает. Мёртвый номер отвечает иначе — «нет такого
// процесса», и только он говорит, что адрес протух.
func dictationAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	handle, _, err := kernel32.NewProc("OpenProcess").Call(0x1000, 0, uintptr(pid))
	if handle != 0 {
		_, _, _ = kernel32.NewProc("CloseHandle").Call(handle)
		return true
	}
	return err == syscall.ERROR_ACCESS_DENIED
}
