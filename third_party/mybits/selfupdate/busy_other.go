//go:build !windows

package selfupdate

import "os"

// busy: вне Windows занятый файл переименованию не мешает.
func busy(error) bool { return false }

// syncDir сбрасывает запись о переименовании на диск; ошибки не важны:
// замена уже состоялась, это лишь страховка от сбоя питания.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
