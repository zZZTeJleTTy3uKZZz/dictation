package localdict

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// historyActionsRoutes — просьбы окна над историей целиком: «Повторить все
// неудачные» и «Подчистить».
//
// Очередь одной фразы живёт в retry.go: там повтор по сроку и ручной повтор.
// Здесь решения принимаются обо ВСЕЙ истории сразу, и правило у них одно:
// человек попросил — повторяем или убираем, а причина каждого пропуска остаётся
// в строке, чтобы неудача не пропала молча.
func (a *App) historyActionsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /history/retry-failed", func(w http.ResponseWriter, r *http.Request) {
		queued, skipped := a.queueFailed()
		jsonResponse(w, map[string]any{"queued": queued, "skipped": skipped})
	})
	mux.HandleFunc("POST /history/cleanup", func(w http.ResponseWriter, r *http.Request) {
		// Ручная очистка тоже удаляет звук: до конца восстановления — отказ.
		if err := a.recoveryGate(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		var data struct {
			// OlderThanDays — убрать записи старше стольких дней.
			OlderThanDays int `json:"older_than_days"`
			// KeepLast — оставить только столько самых новых записей.
			KeepLast int `json:"keep_last"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			http.Error(w, "не разобрать просьбу", http.StatusBadRequest)
			return
		}
		// Ровно одно условие: «старше N дней» и «оставить N» вместе не имеют
		// смысла, а пустая просьба удалила бы всё подряд.
		if (data.OlderThanDays > 0) == (data.KeepLast > 0) {
			http.Error(w, "нужно одно условие: older_than_days или keep_last", http.StatusBadRequest)
			return
		}
		removed, audio, err := a.cleanupHistory(data.OlderThanDays, data.KeepLast)
		if err != nil {
			a.sessionLog("history-cleanup-failed", err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{"removed": removed, "audio": audio})
	})
}

// jsonResponse отдаёт небольшой ответ окну: счётчики, а не состояние.
func jsonResponse(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// queueFailed ставит в очередь все строки с ошибкой, у которых есть звук.
//
// Строка без звука не может быть разобрана заново, и молча пропустить её
// значит оставить человека с кнопкой, которая «ничего не делает». Поэтому
// причина пропуска пишется в ту же строку.
func (a *App) queueFailed() (queued, skipped int) {
	now := time.Now()
	changed := false
	a.mu.Lock()
	for index := range a.state.History {
		entry := &a.state.History[index]
		if entry.Status != statusError {
			continue
		}
		switch {
		case entry.Audio == "":
			entry.Error = "звук не сохранился: повторить нельзя"
			skipped++
			changed = true
		default:
			if info, err := os.Stat(entry.Audio); err != nil || info.IsDir() {
				entry.Error = "файл записи пропал: повторить нельзя"
				skipped++
				changed = true
				continue
			}
			// Новая попытка — новый счёт: у строки, исчерпавшей все автоповторы,
			// прежний счётчик сразу вернул бы её в error.
			entry.Status, entry.Attempts, entry.Error = statusRetrying, 0, ""
			entry.NextRetryAt = now
			queued++
			changed = true
		}
	}
	if changed {
		a.touchHistoryLocked()
	}
	a.mu.Unlock()
	if changed {
		a.saveHistory()
	}
	if queued > 0 {
		a.kickRetry()
	}
	a.sessionLog("history-retry-failed", "queued="+strconv.Itoa(queued)+" skipped="+strconv.Itoa(skipped))
	return queued, skipped
}

// cleanupHistory убирает записи по дате или по числу оставленных и удаляет их
// звук. Метки в журналах ставятся ДО записи истории: иначе сбой между ними
// вернул бы убранное при следующем запуске.
//
// olderThanDays > 0 — решение человека «всё, что старше, мне не нужно»: журналы
// помечаются cleared, а отметка об очистке сдвигается на отсечку.
// keepLast > 0 — предел «оставить последние N»: журналы помечаются trimmed, как
// при вытеснении пределом истории.
func (a *App) cleanupHistory(olderThanDays, keepLast int) (removed, audioFiles int, err error) {
	now := time.Now()
	var cutoff time.Time
	if olderThanDays > 0 {
		cutoff = now.AddDate(0, 0, -olderThanDays)
	}
	a.mu.Lock()
	history := append([]Entry(nil), a.state.History...)
	a.mu.Unlock()
	// Новые сверху: «оставить последние N» значит оставить самые свежие, а не
	// первые попавшиеся по порядку в файле.
	sort.SliceStable(history, func(left, right int) bool {
		return history[left].At.After(history[right].At)
	})
	kept := history[:0]
	var chosen []Entry
	for index, entry := range history {
		discard := false
		switch {
		case olderThanDays > 0:
			discard = !entry.At.IsZero() && entry.At.Before(cutoff)
		case keepLast > 0:
			discard = index >= keepLast
		}
		if discard {
			chosen = append(chosen, entry)
		} else {
			kept = append(kept, entry)
		}
	}
	if len(chosen) == 0 {
		return 0, 0, nil
	}
	if olderThanDays > 0 {
		// Отметка об очистке закрывает восстановлению всё, что старше отсечки:
		// даже сессию, строки которой в истории не было.
		a.mu.Lock()
		a.clearedAt = cutoff
		armed := a.sweepArmedAt
		a.mu.Unlock()
		if err := a.writeState(historyStateFile, historyState{ClearedAt: cutoff, SweepArmedAt: armed}); err != nil {
			return 0, 0, err
		}
	}
	// Метки — первыми, история — после. Обратный порядок воскрешал бы записи.
	for _, entry := range chosen {
		if olderThanDays > 0 {
			a.markJournalCleared(entry)
		} else {
			a.markJournalTrimmed(entry.ID, entry.Audio)
		}
	}
	a.mu.Lock()
	a.state.History = kept
	a.touchHistoryLocked()
	err = a.saveHistoryLocked()
	a.mu.Unlock()
	if err != nil {
		return 0, 0, err
	}
	audioFiles, _ = a.dropAudio(chosen, kept)
	a.sessionLog("history-cleanup", "removed="+strconv.Itoa(len(chosen))+" audio="+strconv.Itoa(audioFiles))
	return len(chosen), audioFiles, nil
}

// dropAudio удаляет звук убранных строк и возвращает число файлов и байтов.
//
// Общее ядро удаления звука: и ручная «Подчистить» (строки уходят целиком), и
// автоочистка по сроку (строка остаётся с текстом, звук — нет). Звук, на который
// ссылается хотя бы одна оставшаяся строка, не трогаем, а путь проверяем по
// audioDeletable: из истории он может прийти любой.
//
// Вызывать ПОСЛЕ записи истории: пока строка на диске, файл ей нужен, а сбой
// между ними оставит звук, который уберёт уборка сирот.
func (a *App) dropAudio(chosen, kept []Entry) (files int, bytes int64) {
	referenced := map[string]bool{}
	for _, entry := range kept {
		if entry.Audio != "" {
			referenced[audioKey(entry.Audio)] = true
		}
	}
	for _, entry := range chosen {
		if entry.Audio == "" || referenced[audioKey(entry.Audio)] || !a.audioDeletable(entry.Audio) {
			continue
		}
		var size int64
		if info, err := os.Stat(entry.Audio); err == nil {
			size = info.Size()
		}
		if os.Remove(entry.Audio) == nil {
			files++
			bytes += size
		}
	}
	return files, bytes
}

// markJournalCleared ставит cleared журналу убранной по дате строки.
//
// Отличие от markJournalsCleared в том, что здесь решается судьба ОДНОЙ строки:
// очистка истории целиком метит всё живое, а «подчистить старше недели» — только
// то, что убрано.
func (a *App) markJournalCleared(entry Entry) {
	path := a.findJournal(entry.ID, entry.Audio)
	if path == "" {
		return
	}
	if !markCleared(path) {
		a.sessionLog("journal-mark-failed", "id="+entry.ID)
	}
}

// audioKey — по какому ключу сравниваются пути звука. Windows не различает
// регистр, поэтому один и тот же файл не должен выглядеть двумя.
func audioKey(path string) string {
	return strings.ToLower(filepath.Clean(path))
}

// audioDeletable — можно ли удалять этот звук: только свой, внутри папки данных
// или записей, и только звуковой файл. Путь приходит из истории, но проверять
// его всё равно надо: старые файлы могли записать что угодно.
func (a *App) audioDeletable(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	clean := audioKey(path)
	inside := false
	for _, root := range []string{a.Dir, a.Media} {
		if strings.TrimSpace(root) == "" {
			continue
		}
		root = audioKey(root)
		if strings.HasPrefix(clean, root+string(filepath.Separator)) {
			inside = true
			break
		}
	}
	if !inside {
		return false
	}
	switch strings.ToLower(filepath.Ext(clean)) {
	case ".wav", ".mp3", ".m4a", ".ogg", ".opus", ".flac", ".aac", ".webm":
		return true
	}
	return false
}
