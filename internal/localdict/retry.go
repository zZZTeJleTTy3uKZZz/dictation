// ЗАПИСЬ, КОТОРАЯ НЕ УШЛА, ЖДЁТ СВОЕГО ЧАСА.
//
// Облако отказывает по пустякам: обрыв соединения на полпути, предел частоты,
// пятисотый ответ. Раньше один такой отказ кончал диктовку: файл фразы оставался
// на диске, служба с ним больше ничего не делала, а в истории не было ни строки,
// и попросить ещё раз было негде.
//
// Теперь очередь одна и живёт в истории:
//
//   - не вышло сразу — строка встаёт со статусом retrying и звуком, причина
//     написана словами, время попытки — в next_retry_at;
//   - служба сама повторяет, когда подходит срок: 30 с, 2 мин, 10 мин, 30 мин,
//     2 ч. Перед попыткой — лёгкий вопрос облаку, чтобы не тратить попытку без
//     сети; такая проверка попытку не расходует;
//   - повторы идут одной нитью и переживают перезапуск службы: при старте
//     строки retrying снова встают в очередь, а оборванный разбор
//     (transcribing) возвращается в неё при облаке;
//   - автоповтор кладёт текст в историю и показывает уведомление, но НЕ трогает
//     буфер обмена: человек мог скопировать туда своё;
//   - ручной повтор (POST /retry) работает в любой момент, даже для error, и
//     кладёт текст в буфер — человек попросил сам;
//   - файл фразы не удаляется, пока строка не распознана; после успеха — удаляется.
package localdict

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/providercfg"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
)

// retryDelays — паузы между автопопытками. Пять попыток, а не бесконечный
// цикл: сервис, который не ответил столько раз, не ответит и на шестой, а
// запись всё это время лежит на диске и никуда не денется.
var retryDelays = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour}

// retryTimeout — срок на один ручной разбор по просьбе из окна.
const retryTimeout = 2 * time.Minute

// failedDestination — строка истории, у которой звук есть, а текста нет. Поле
// остаётся ради старого окна: оно ищет кнопку повтора по этому значению.
const failedDestination = "failed"

// retryPauseAfter — пауза перед следующей автопопыткой после k неудач.
func (a *App) retryPauseAfter(k int) time.Duration {
	pauses := a.retryPauses
	if len(pauses) == 0 {
		pauses = retryDelays
	}
	if k < 0 {
		k = 0
	}
	if k >= len(pauses) {
		return pauses[len(pauses)-1]
	}
	return pauses[k]
}

// pauseFor — сколько ждать до следующей попытки.
//
// Обычно — расписание строки. Но у предела частоты сервис сам называет срок в
// Retry-After своего ответа: прийти к нему раньше значило бы получить второй
// отказ и истратить попытку зря. Срок берётся только у 429 — у прочих отказов
// заголовок ничего не значит.
func (a *App) pauseFor(attempt int, cause error) time.Duration {
	if wait := speech.RetryAfterOf(cause); wait > 0 {
		return wait
	}
	return a.retryPauseAfter(attempt)
}

// retryPauseCount — сколько автопопыток разрешено.
func (a *App) retryPauseCount() int {
	if len(a.retryPauses) > 0 {
		return len(a.retryPauses)
	}
	return len(retryDelays)
}

// probeDelay — пауза, когда облако не ответило на лёгкую проверку.
func (a *App) probeDelay() time.Duration {
	if a.retryProbeDelay > 0 {
		return a.retryProbeDelay
	}
	return 30 * time.Second
}

// countPending — сколько строк ещё не разобрано: ждёт повтора или разбирается
// прямо сейчас.
func countPending(history []Entry) int {
	pending := 0
	for _, entry := range history {
		if entry.Status == statusRetrying || entry.Status == statusTranscribing {
			pending++
		}
	}
	return pending
}

// touchHistoryLocked отмечает правку истории. Зовётся под a.mu.
func (a *App) touchHistoryLocked() {
	a.state.HistoryRev++
	a.state.Pending = countPending(a.state.History)
}

// saveHistory пишет копию истории на диск ВНЕ a.mu: снимок снимается под
// замком, запись идёт после. 250 КБ с диском под замком держали Snapshot и
// окно, а теперь замок отпускается сразу.
//
// Порядок записей сохраняет номер версии: две нити могут снять снимки
// по-разному, и старая версия не должна лечь поверх новой.
func (a *App) saveHistory() error {
	a.mu.Lock()
	rev := a.state.HistoryRev
	history := append([]Entry(nil), a.state.History...)
	a.mu.Unlock()
	return a.writeHistory(rev, history)
}

// historyWriteHook — точка наблюдения для проверки: запись истории обязана
// идти без a.mu. В работе всегда nil.
var historyWriteHook func()

// historyFile — запись истории на диск. В проверке подменяется отказом: сбой
// между history.json и меткой в журнале не должен терять запись.
var historyFile func(path string, value any) error

func writeHistoryFile(path string, value any) error {
	if historyFile != nil {
		return historyFile(path, value)
	}
	return writeJSON(path, value)
}

func (a *App) writeHistory(rev int64, history []Entry) error {
	if historyWriteHook != nil {
		historyWriteHook()
	}
	a.historyWriteMu.Lock()
	defer a.historyWriteMu.Unlock()
	if rev < a.historyWritten {
		return nil
	}
	if err := writeHistoryFile(filepath.Join(a.Dir, "history.json"), history); err != nil {
		a.sessionLog("history-write-failed", err.Error())
		return err
	}
	a.historyWritten = rev
	// Вытесненные пределом строки метятся в журналах ПОСЛЕ записи истории:
	// метка — обещание, что строка не вернётся, и ставить её раньше незачем.
	// Здесь же убирается их звук: строки больше нет, и держать файл незачем.
	a.markTrimmedPending(history)
	return nil
}

// markTrimmedPending ставит журналам метку trimmed за строки, вытесненные
// пределом истории, и удаляет звук, на который больше никто не ссылается.
// Метки копились под a.mu (trimHistoryLocked), а диск идёт здесь: writeHistory
// зовут и под замком, поэтому у карты свой trimMu, а ссылки считаются по тому
// срезу истории, который только что записан, — без второго замка.
func (a *App) markTrimmedPending(kept []Entry) {
	a.trimMu.Lock()
	pending := a.trimmedPending
	a.trimmedPending = nil
	a.trimMu.Unlock()
	if len(pending) == 0 {
		return
	}
	referenced := map[string]bool{}
	for _, entry := range kept {
		if entry.Audio != "" {
			referenced[audioKey(entry.Audio)] = true
		}
	}
	removed := 0
	for id, audio := range pending {
		a.markJournalTrimmed(id, audio)
		if audio == "" || referenced[audioKey(audio)] || !a.audioDeletable(audio) {
			continue
		}
		if os.Remove(audio) == nil {
			removed++
		}
	}
	if removed > 0 {
		a.sessionLog("trimmed-audio", "count="+strconv.Itoa(removed))
	}
}

// findEntryLocked ищет строку по звуку. Зовётся под a.mu.
func (a *App) findEntryLocked(path string) *Entry {
	if path == "" {
		// Пустой путь не совпадает ни с чем: у обычной записи audio пуст, и
		// искать по нему значило бы принять её за разбираемую фразу.
		return nil
	}
	for index := range a.state.History {
		if samePath(a.state.History[index].Audio, path) {
			return &a.state.History[index]
		}
	}
	return nil
}

// keepUnsentPhrase кладёт нераспознанную фразу в историю со статусом retrying
// и будит нить повтора. Звук остаётся на диске: он и есть то, что можно разобрать
// ещё раз.
func (a *App) keepUnsentPhrase(path string, cause error) {
	short, _ := cloudTrouble(cause)
	if path == "" {
		// Разбирать нечего: файла нет. Строка всё равно нужна, чтобы отказ не
		// пропал, но ждать повтора ей не с чем.
		a.mu.Lock()
		at := time.Now()
		a.state.History = a.trimHistoryLocked(append([]Entry{{
			ID: entryID("", at), At: at, Seconds: a.state.Seconds, Destination: failedDestination,
			Status: statusError, Error: "файл фразы не сохранён",
		}}, a.state.History...))
		a.touchHistoryLocked()
		a.mu.Unlock()
		a.saveHistory()
		a.sessionLog("retry-unsent", "file=none reason=no-audio")
		return
	}
	if _, err := os.Stat(path); err != nil {
		short = "файл фразы пропал"
	}
	a.mu.Lock()
	entry := a.findEntryLocked(path)
	if entry == nil {
		at := time.Now()
		a.state.History = a.trimHistoryLocked(append([]Entry{{
			ID: entryID(path, at), At: at, Seconds: a.state.Seconds, Destination: failedDestination,
			Audio: path, Status: statusRetrying, NextRetryAt: at.Add(a.retryPauseAfter(0)), Error: short,
		}}, a.state.History...))
	} else {
		entry.Status, entry.Destination, entry.Error = statusRetrying, failedDestination, short
		entry.ID = entryID(path, entry.At)
		if entry.NextRetryAt.IsZero() {
			entry.NextRetryAt = time.Now().Add(a.retryPauseAfter(entry.Attempts))
		}
	}
	a.touchHistoryLocked()
	a.mu.Unlock()
	a.saveHistory()
	a.kickRetry()
	a.sessionLog("retry-unsent", "file="+filepath.Base(path)+" reason="+short)
}

// keepFailedPhrase кладёт фразу в историю со статусом error: её пробовали
// местной моделью, и повторять сами мы не будем. Человек нажмёт «ещё раз».
func (a *App) keepFailedPhrase(path string) {
	a.mu.Lock()
	entry := a.findEntryLocked(path)
	if entry == nil {
		at := time.Now()
		a.state.History = a.trimHistoryLocked(append([]Entry{{
			ID: entryID(path, at), At: at, Seconds: a.state.Seconds, Destination: failedDestination,
			Audio: path, Status: statusError, Error: "фраза не распознана",
		}}, a.state.History...))
	} else {
		entry.ID = entryID(path, entry.At)
		entry.Status, entry.Destination = statusError, failedDestination
		if entry.Error == "" {
			entry.Error = "фраза не распознана"
		}
		entry.NextRetryAt = time.Time{}
	}
	a.touchHistoryLocked()
	a.mu.Unlock()
	a.saveHistory()
	a.sessionLog("phrase-kept", "file="+filepath.Base(path))
}

// resolvePhrase ставит распознанный текст в строку истории этой фразы (или
// заводит строку, если её нет), снимает статус ожидания и убирает звук фразы.
// Запись разговора (recordings) остаётся: её слушают и после текста.
func (a *App) resolvePhrase(path, text, destination string) {
	text = strings.TrimSpace(text)
	if path == "" {
		return
	}
	isPhrase := strings.HasPrefix(filepath.Base(path), "phrase-")
	// Звук убирается ДО того, как текст станет виден в истории: иначе окно
	// увидит «распознано», а файл ещё лежит — и проверка (и человек) застанет
	// момент, когда строке уже не нужен звук, а он ещё есть.
	if isPhrase {
		_ = os.Remove(path)
	}
	a.mu.Lock()
	found := false
	for index := range a.state.History {
		entry := &a.state.History[index]
		if !samePath(entry.Audio, path) {
			continue
		}
		entry.ID = entryID(entry.Audio, entry.At)
		entry.Text, entry.Destination = text, destination
		entry.Status, entry.Attempts, entry.NextRetryAt, entry.Error = statusDone, 0, time.Time{}, ""
		if isPhrase {
			entry.Audio = ""
		}
		found = true
		break
	}
	if !found {
		at := time.Now()
		entry := Entry{ID: entryID(path, at), At: at, Text: text, Destination: destination}
		if !isPhrase {
			entry.Audio = path
		}
		a.state.History = a.trimHistoryLocked(append([]Entry{entry}, a.state.History...))
	}
	if strings.TrimSpace(a.state.Trouble) != "" && a.state.Stage != "recording" {
		a.state.Trouble = ""
	}
	a.touchHistoryLocked()
	a.mu.Unlock()
	a.saveHistory()
}

// kickRetry будит нить повтора, не дожидаясь такта.
func (a *App) kickRetry() {
	if a.retryWake == nil {
		return
	}
	select {
	case a.retryWake <- struct{}{}:
	default:
	}
}

// retryLoop проверяет сроки повторов. Одна нить на всю службу: две попытки
// одной фразы зараз — это два платных запроса и два разных текста.
func (a *App) retryLoop(ctx context.Context) {
	every := a.retryEvery
	if every <= 0 {
		every = time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.retryWake:
			a.retryDue(ctx)
		case <-ticker.C:
			a.retryDue(ctx)
		}
	}
}

// retryDue разбирает все строки, чей срок подошёл.
func (a *App) retryDue(ctx context.Context) {
	// Очередь ждёт именно облако: при местном способе строки остаются как есть
	// и человек разбирает их сам — подменять его выбор мы не вправе.
	if !cloudSpeechChosen(a.voice()) {
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		path, ok := a.nextDue()
		if !ok {
			return
		}
		_, _ = a.attemptRetry(ctx, path, false)
	}
}

// nextDue — звук первой строки, которой пора повториться.
func (a *App) nextDue() (string, bool) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for index := range a.state.History {
		entry := &a.state.History[index]
		if entry.Status != statusRetrying {
			continue
		}
		if entry.NextRetryAt.IsZero() || !entry.NextRetryAt.After(now) {
			return entry.Audio, entry.Audio != ""
		}
	}
	return "", false
}

// probeCloud — ответило ли облако на лёгкий вопрос. Подмена в проверках — поле
// cloudProbe.
func (a *App) probeCloud(ctx context.Context) bool {
	if a.cloudProbe != nil {
		return a.cloudProbe(ctx)
	}
	return a.cloudReachable(ctx)
}

// attemptRetry делает одну попытку разбора строки истории.
//
// manual=true — человек нажал «Распознать ещё раз»: текст сразу в буфер, а
// следующий срок сбрасывается. manual=false — автоповтор: буфер не трогаем,
// показываем уведомление.
func (a *App) attemptRetry(ctx context.Context, path string, manual bool) (string, error) {
	if path == "" {
		return "", errors.New("у строки нет звука")
	}
	// Сперва проверка облака: без сети попытку тратить незачем. Срок следующей
	// попытки при этом не растёт — сеть вернётся, и мы просто спросим снова.
	if !manual && !a.probeCloud(ctx) {
		a.retryDeferred(path, providercfg.Verdict(providercfg.KindNetwork).Reason)
		a.sessionLog("retry-skipped", "file="+filepath.Base(path)+" reason=cloud-unreachable")
		return "", cloudRefusal(nil)
	}
	a.retryGate.Lock()
	defer a.retryGate.Unlock()

	now := time.Now()
	a.mu.Lock()
	entry := a.findEntryLocked(path)
	wasFailed := entry != nil && (entry.Status == statusError || entry.Status == statusDone)
	// Строка уже с текстом — человек переотправил удачную запись. Неудача
	// такой попытки не должна превращать её в «не отправлено»: текст на месте,
	// и пометка о сбое рядом с ним только путает.
	var settled *Entry
	if entry != nil && entry.Status == statusDone && strings.TrimSpace(entry.Text) != "" {
		kept := *entry
		settled = &kept
	}
	if entry != nil {
		if !manual {
			if entry.Status != statusRetrying {
				a.mu.Unlock()
				return "", nil
			}
			// Срок могли сдвинуть, пока ждали замок.
			if !entry.NextRetryAt.IsZero() && now.Before(entry.NextRetryAt) {
				a.mu.Unlock()
				return "", nil
			}
		}
		entry.Status = statusTranscribing
		a.touchHistoryLocked()
	}
	a.mu.Unlock()
	a.saveHistory()

	text, err := a.recognizePhrase(ctx, path)
	text = strings.TrimSpace(text)
	if err == nil && text != "" {
		a.resolvePhrase(path, text, "clipboard")
		if manual {
			// Ручной повтор — просьба человека: он ждёт текст в буфере.
			if copyErr := clipboardWrite(text); copyErr != nil {
				a.sessionLog("clipboard-failed", copyErr.Error())
			}
		} else {
			// Автоповтор буфер не трогает: человек мог скопировать туда своё.
			a.showNotice("Диктовка", restoredNotice(text))
		}
		a.sessionLog("retry-done", fmt.Sprintf("manual=%t words=%d", manual, len(strings.Fields(text))))
		return text, nil
	}
	if err == nil {
		err = errCloudEmpty
	}
	if settled != nil {
		a.restoreSettled(path, *settled)
		a.sessionLog("retry-failed", fmt.Sprintf("manual=%t kept=settled reason=%s", manual, shortReason(err)))
		return "", err
	}
	a.retryFailed(path, err, manual, wasFailed)
	return "", err
}

// restoreSettled возвращает удачной строке то, что было до неудачной
// переотправки.
func (a *App) restoreSettled(path string, kept Entry) {
	a.mu.Lock()
	if entry := a.findEntryLocked(path); entry != nil {
		*entry = kept
		a.touchHistoryLocked()
	}
	a.mu.Unlock()
	a.saveHistory()
}

// retryDeferred отмечает, что попытка не состоялась из-за облака: статус
// остаётся retrying, счётчик попыток не растёт.
func (a *App) retryDeferred(path, reason string) {
	a.mu.Lock()
	entry := a.findEntryLocked(path)
	if entry != nil {
		entry.Status, entry.Error = statusRetrying, reason
		entry.NextRetryAt = time.Now().Add(a.probeDelay())
		a.touchHistoryLocked()
	}
	a.mu.Unlock()
	a.saveHistory()
}

// retryFailed записывает исход неудачной попытки: транзиентный отказ ждёт
// следующего срока, неисправимый или исчерпанный — становится error.
func (a *App) retryFailed(path string, cause error, manual, wasFailed bool) {
	short, _ := cloudTrouble(cause)
	a.mu.Lock()
	entry := a.findEntryLocked(path)
	switch {
	case entry == nil && manual:
		// Строки не было — заводим, чтобы отказ не потерялся.
		at := time.Now()
		a.state.History = a.trimHistoryLocked(append([]Entry{{
			ID: entryID(path, at), At: at, Destination: failedDestination, Audio: path,
			Status: statusError, Attempts: 1, Error: short,
		}}, a.state.History...))
	case entry == nil:
		// Автоповтор без строки — разбирать нечего.
	case manual:
		entry.Attempts++
		entry.Error = short
		if wasFailed {
			entry.Status, entry.NextRetryAt = statusError, time.Time{}
		} else {
			// Ручная попытка не сжигает автоповторы: срок начинается заново. И
			// если сервис сам назвал срок (Retry-After своего ответа), он главнее
			// расписания: прийти раньше — второй отказ и истраченное окно.
			wait := speech.RetryAfterOf(cause)
			if wait <= 0 {
				wait = a.retryPauseAfter(1)
			}
			entry.Status, entry.Attempts = statusRetrying, 1
			entry.NextRetryAt = time.Now().Add(wait)
		}
	default:
		entry.Attempts++
		entry.Error = short
		// Транзиентный отказ — 5xx, 408, 425, таймаут, срыв сети, недоступность,
		// предел частоты — ждёт следующего срока. Ключ (401/403) и прочие отказы
		// делают строку окончательной: пауза их не исправит.
		if !providercfg.Transient(cause) || entry.Attempts >= a.retryPauseCount() {
			entry.Status, entry.NextRetryAt = statusError, time.Time{}
		} else {
			entry.Status = statusRetrying
			entry.NextRetryAt = time.Now().Add(a.pauseFor(entry.Attempts, cause))
		}
	}
	a.touchHistoryLocked()
	a.mu.Unlock()
	a.saveHistory()
	a.sessionLog("retry-failed", fmt.Sprintf("manual=%t attempts=%d reason=%s", manual, a.attemptsOf(path), shortReason(cause)))
}

// attemptsOf — сколько попыток у строки, для журнала.
func (a *App) attemptsOf(path string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if entry := a.findEntryLocked(path); entry != nil {
		return entry.Attempts
	}
	return 0
}

// restoreUnsent поднимает очередь после перезапуска службы: строки, ждавшие
// повтора, снова ждут. Оборванный разбор (transcribing) возвращается в очередь,
// только если очередь вообще идёт — при облаке; при местном способе человек
// разбирает сам, и вечное «расшифровывается» ему ни к чему.
func (a *App) restoreUnsent() {
	now := time.Now()
	retryable := cloudSpeechChosen(a.voice())
	changed, restored := false, 0
	a.mu.Lock()
	for index := range a.state.History {
		entry := &a.state.History[index]
		if entry.Status != statusRetrying && entry.Status != statusTranscribing {
			continue
		}
		restored++
		if entry.Status == statusTranscribing {
			if entry.Recovered {
				// Восстановленную запись разберёт восстановление (recoverRecordings):
				// журнал ещё не помечен, и оно само поставит done или error.
				continue
			}
			if retryable {
				entry.Status, entry.NextRetryAt = statusRetrying, now
			} else {
				entry.Status, entry.NextRetryAt = statusError, time.Time{}
				entry.Error = "расшифровка прервана перезапуском"
			}
			changed = true
		}
		if entry.NextRetryAt.IsZero() {
			entry.NextRetryAt = now
			changed = true
		}
		if entry.Audio == "" {
			entry.Status, entry.NextRetryAt = statusError, time.Time{}
			entry.Error = "файл записи пропал"
			changed = true
			continue
		}
		if _, err := os.Stat(entry.Audio); err != nil {
			entry.Status, entry.NextRetryAt = statusError, time.Time{}
			entry.Error = "файл записи пропал"
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
	if restored > 0 {
		a.sessionLog("retry-restored", fmt.Sprintf("count=%d", restored))
	}
}

// retryAudioAllowed — окно просит разобрать только свои файлы: фразы рядом с
// историей и записи разговоров, а не любой путь на машине.
func (a *App) retryAudioAllowed(path string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" || !filepath.IsAbs(clean) {
		return "", errors.New("нужен полный путь к записи")
	}
	switch strings.ToLower(filepath.Ext(clean)) {
	case ".m4a", ".wav", ".mp3", ".webm", ".ogg", ".flac":
	default:
		return "", errors.New("это не запись звука")
	}
	inside := false
	for _, root := range []string{a.Dir, a.Media} {
		if root == "" {
			continue
		}
		if rel, err := filepath.Rel(filepath.Clean(root), clean); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			inside = true
			break
		}
	}
	if !inside {
		return "", errors.New("запись не из папок диктовки")
	}
	if info, err := os.Stat(clean); err != nil || info.IsDir() {
		return "", errors.New("файла записи уже нет")
	}
	return clean, nil
}

// retryRoute — POST /retry {"audio": путь}: «Распознать ещё раз» из окна.
func (a *App) retryRoute(mux *http.ServeMux, respond func(http.ResponseWriter)) {
	mux.HandleFunc("POST /retry", func(w http.ResponseWriter, r *http.Request) {
		var data struct {
			Audio string `json:"audio"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		if json.NewDecoder(r.Body).Decode(&data) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		// Разбор с повторами дольше общего срока ответа сервера (10 с).
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(retryTimeout + 30*time.Second))
		path, err := a.retryAudioAllowed(data.Audio)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(a.ctx, retryTimeout)
		text, err := a.attemptRetry(ctx, path, true)
		cancel()
		a.sessionLog("speech-retry-manual", fmt.Sprintf("ok=%t words=%d", err == nil && text != "", len(strings.Fields(text))))
		if err != nil || strings.TrimSpace(text) == "" {
			_, full := cloudTrouble(err)
			http.Error(w, full+" Попробуйте ещё раз чуть позже.", http.StatusBadGateway)
			return
		}
		respond(w)
	})
}

// adoptLeftPhrases — фразы, оставшиеся от прежних запусков: файл phrase-* лежит,
// а в истории его нет (до этой версии отказ в историю не попадал). Они встают в
// историю со статусом error по своему времени и ждут кнопки «Отправить ещё раз».
//
// Сами по себе они не разбираются: служба уже пробовала и не смогла, а
// повторять без просьбы человека значит тратить его трафик и ключ.
func (a *App) adoptLeftPhrases() {
	files, _ := filepath.Glob(filepath.Join(a.Dir, "phrase-*"))
	if len(files) == 0 {
		return
	}
	a.mu.Lock()
	added := 0
	for _, file := range files {
		if a.findEntryLocked(file) != nil {
			continue
		}
		info, err := os.Stat(file)
		if err != nil || info.IsDir() {
			continue
		}
		a.state.History = append(a.state.History, Entry{
			ID: entryID(file, info.ModTime()), At: info.ModTime(), Destination: failedDestination,
			Audio: file, Status: statusError, Error: "фраза осталась от прежнего запуска",
		})
		added++
	}
	if added > 0 {
		sort.SliceStable(a.state.History, func(i, j int) bool { return a.state.History[i].At.After(a.state.History[j].At) })
		a.state.History = a.trimHistoryLocked(a.state.History)
		a.touchHistoryLocked()
	}
	a.mu.Unlock()
	if added > 0 {
		a.saveHistory()
		a.sessionLog("phrases-adopted", fmt.Sprintf("count=%d", added))
	}
}

// restoredNotice — слова уведомления о разборе после восстановления связи.
func restoredNotice(text string) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	const shown = 5
	head, ellipsis := words, ""
	if len(head) > shown {
		head, ellipsis = head[:shown], "…"
	}
	return "Распознано после восстановления связи: " + strings.Join(head, " ") + ellipsis + " (текст в истории)"
}

// showNotice говорит человеку то, что случилось, пока он не смотрел. Способ
// подменяется в проверках: настоящее уведомление рисует Windows, и в проверке
// ему не место.
func (a *App) showNotice(title, text string) {
	if text == "" {
		return
	}
	if a.notify != nil {
		a.notify(title, text)
		return
	}
	window := a.overlayWindow()
	if window == 0 {
		return
	}
	balloon(window, title, text)
}
