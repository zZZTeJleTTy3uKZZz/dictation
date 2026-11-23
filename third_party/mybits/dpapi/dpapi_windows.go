//go:build windows

package dpapi

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const supported = true

func crypt(input []byte, decrypt bool) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(input))}
	if len(input) > 0 {
		in.Data = &input[0]
	}
	var out windows.DataBlob
	var err error
	if decrypt {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	runtime.KeepAlive(input)
	if err != nil {
		return nil, err
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) }()
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}
