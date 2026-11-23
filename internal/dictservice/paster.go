package dictservice

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

// pasterBackoff — как часто служба пытается поднимать помощника, если его нет:
// повышенный токен есть не у всех, и каждые пять секунд писать отказ в журнал
// незачем.
const (
	pasterBackoff = 15 * time.Second
	pasterRetry   = 5 * time.Second
)

// ensurePaster держит узкого помощника вставки живым ровно в том сеансе, где
// человек вошёл. Повышение получает только он: сеансовая часть остаётся
// среднего уровня и не может быть использована как мост к admin-окнам.
func (h *handler) ensurePaster() {
	if h.stopping.Load() {
		return
	}
	// Сеанс проверяем КАЖДЫЙ такт, даже когда помощник жив: выход человека
	// или переключение сеанса обязаны погасить повышенный процесс.
	session := windows.WTSGetActiveConsoleSessionId()
	if session != h.paster && h.pasterProc != 0 {
		h.stopPaster("session-changed")
	}
	if session == noSession {
		h.stopPaster("logout")
		h.pasterNext = time.Now().Add(pasterRetry)
		return
	}
	if time.Now().Before(h.pasterNext) {
		return
	}
	if h.pasterProc != 0 {
		state, err := windows.WaitForSingleObject(h.pasterProc, 0)
		if err == nil && state == windows.WAIT_OBJECT_0 {
			_ = windows.CloseHandle(h.pasterProc)
			h.pasterProc, h.paster = 0, 0
		} else {
			return // жив
		}
	}
	var user windows.Token
	if err := windows.WTSQueryUserToken(session, &user); err != nil {
		serviceLogf("paster-token-unavailable: " + err.Error())
		h.pasterNext = time.Now().Add(pasterBackoff)
		return
	}
	defer func() { _ = user.Close() }()

	elevated, err := elevatedUserToken(user)
	if err != nil {
		if errors.Is(err, errNoLinkedToken) {
			serviceLogf("event=paster-token elevated=false reason=no-linked-token")
		} else {
			serviceLogf("event=paster-token elevated=false reason=" + err.Error())
		}
		h.pasterNext = time.Now().Add(pasterBackoff)
		return
	}
	defer func() { _ = elevated.Close() }()
	serviceLogf("event=paster-token elevated=true")

	handle, err := startWithToken(session, h.exe, elevated, "--paster")
	if err != nil {
		serviceLogf("paster-start-failed: " + err.Error())
		h.pasterNext = time.Now().Add(pasterRetry)
		return
	}
	// Ушёл сразу — значит канал уже занят старым помощником; не поднимаем
	// копию каждый такт.
	if state, waitErr := windows.WaitForSingleObject(handle, 1500); waitErr == nil && state == windows.WAIT_OBJECT_0 {
		_ = windows.CloseHandle(handle)
		h.pasterNext = time.Now().Add(time.Minute)
		return
	}
	h.pasterProc, h.paster = handle, session
}

// stopPaster завершает помощника: выход человека из сеанса, смена сеанса или
// остановка службы не должны оставлять повышенный процесс живым.
func (h *handler) stopPaster(reason string) {
	if h.pasterProc == 0 {
		return
	}
	_ = windows.TerminateProcess(h.pasterProc, 0)
	_ = windows.CloseHandle(h.pasterProc)
	h.pasterProc, h.paster = 0, 0
	serviceLogf("paster-stopped reason=" + reason)
}
