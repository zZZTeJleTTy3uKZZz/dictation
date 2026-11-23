//go:build !windows

package atomicfile

import "os"

// syncDir сбрасывает запись каталога: без неё после сбоя питания на ext4
// переименование может пропасть.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// busy — вне Windows замена не упирается в открытые дескрипторы.
func busy(error) bool { return false }
