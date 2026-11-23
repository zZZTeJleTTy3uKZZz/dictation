package dictpaster

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// requestTimeout — вставка идёт в нити диктовки: если помощник занят или не
// поднят, фраза не должна ждать его дольше running паузы.
const requestTimeout = 400 * time.Millisecond

// Request просит помощника нажать Ctrl+V в окне window. Возвращает ok или
// причину отказа. Текст не передаётся: он уже лежит в буфере обмена.
func Request(window uintptr) (bool, string) {
	name, err := PipeName()
	if err != nil {
		return false, "unavailable"
	}
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, "unavailable"
	}
	deadline := time.Now().Add(requestTimeout)
	var pipe windows.Handle
	for {
		pipe, err = windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
			windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) || !time.Now().Before(deadline) {
			return false, "unavailable"
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer func() { _ = windows.CloseHandle(pipe) }()

	mode := uint32(windows.PIPE_READMODE_MESSAGE)
	if err := windows.SetNamedPipeHandleState(pipe, &mode, nil, nil); err != nil {
		return false, "unavailable"
	}
	command := "PASTE " + strconv.FormatUint(uint64(window), 10)
	var sent uint32
	if err := windows.WriteFile(pipe, []byte(command), &sent, nil); err != nil {
		return false, "unavailable"
	}
	answer := make([]byte, maxCommandBytes)
	var received uint32
	if err := windows.ReadFile(pipe, answer, &received, nil); err != nil {
		return false, "unavailable"
	}
	reply := strings.TrimSpace(string(answer[:received]))
	if reply == "ok" {
		return true, "sent"
	}
	if reason, ok := strings.CutPrefix(reply, "refused "); ok && reason != "" {
		return false, reason
	}
	return false, "bad-reply"
}
