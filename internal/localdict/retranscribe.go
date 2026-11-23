package localdict

import (
	"context"
	"strings"
	"time"
)

// retranscribe разбирает запись истории заново — в фоне, по просьбе окна.
//
// Звук уже на диске: человек мог потерять текст (отказал распознаватель, не было
// сети, текст стёрли), но голос остался, и повторный разбор возвращает его.
// Ошибка не удаляет запись: причина ложится в error, и человек может попробовать
// ещё раз.
func (a *App) retranscribe(id, audio string) {
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	result, err := a.TranscribeFile(ctx, audio)
	a.mu.Lock()
	defer a.mu.Unlock()
	index, found := a.findHistoryLocked(id)
	if !found {
		return
	}
	entry := &a.state.History[index]
	switch {
	case err != nil:
		entry.Status, entry.Error = statusError, failureReason(err)
	case strings.TrimSpace(result.Text) == "":
		entry.Status, entry.Error = statusError, "распознавание не удалось"
	default:
		entry.Status, entry.Error = statusDone, ""
		entry.Text = strings.TrimSpace(result.Text)
		if result.Seconds > 0 {
			entry.Seconds = result.Seconds
		}
	}
	if err := a.saveHistoryLocked(); err != nil {
		a.sessionLog("history-write-failed", err.Error())
	}
	a.sessionLog("retranscribe", "id="+id+" status="+entry.Status)
}
