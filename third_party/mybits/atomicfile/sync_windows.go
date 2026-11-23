//go:build windows

package atomicfile

import (
	"errors"
	"syscall"
)

const (
	errorAccessDenied     = syscall.Errno(5)
	errorSharingViolation = syscall.Errno(32)
	errorLockViolation    = syscall.Errno(33)
)

// busy — файл держит чужой дескриптор: замену стоит повторить.
func busy(err error) bool {
	return errors.Is(err, errorAccessDenied) || errors.Is(err, errorSharingViolation) || errors.Is(err, errorLockViolation)
}

// syncDir на Windows не нужен: каталог открыть на запись нельзя, а NTFS
// журналирует переименование сама.
func syncDir(string) {}
