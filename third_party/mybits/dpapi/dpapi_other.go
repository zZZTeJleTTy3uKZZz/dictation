//go:build !windows

package dpapi

const supported = false

func crypt([]byte, bool) ([]byte, error) { return nil, ErrUnsupported }
