//go:build !windows && !linux

package mediapause

// Supported — есть ли на этой системе чем управлять плеерами.
func Supported() bool { return false }

// NewDefault — бэкенд системы; здесь его нет.
func NewDefault() (Backend, error) { return nil, ErrUnsupported }
