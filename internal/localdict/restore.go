package localdict

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/aacrecover"
)

// restoreAfter — сколько файл должен пролежать нетронутым, прежде чем считать
// его брошенным. Идущая запись растёт прямо сейчас, и её WAV без обновлённого
// заголовка — нормальное состояние.
const restoreAfter = time.Minute

const (
	// restorePerStart — потолок восстановления за один запуск. На машине
	// владельца 233 записи и 200 журналов: без потолка первый же старт разобрал
	// бы их все разом и ушёл в облако сотней запросов.
	restorePerStart = 20
	// restoreAudioAge — сколько звук остаётся годным для повторного разбора без
	// текста. Старше недели — это уже не потеря, а архив: человек может
	// разобрать его сам кнопкой «Расшифровать заново».
	restoreAudioAge = 7 * 24 * time.Hour
)

// recoverAtStart заводит в истории записи, которых там нет: по журналам — с
// текстом, который успел дойти до диска, по файлам записей — со статусом
// transcribing, чтобы разбор прошёл в фоне.
//
// Идёт ПРИ ЗАПУСКЕ и без распознавателя: текст из журнала виден сразу, а
// разбирать звук — дело recoverInterrupted. Журнал с меткой finish или
// recovered здесь не пропускается: метка говорит, что разговор был разобран, а
// не что его строка есть в history.json. Именно это расхождение и потеряло
// запись 20261111-231748.
//
// Восстанавливается только то, что новее водяной отметки: сессии старее самой
// старой строки истории вытеснил бы её предел, и возвращать их — значит
// возвращать их при каждом запуске.
func (a *App) recoverAtStart() {
	a.dropDiscardedHistory()
	if count := a.markTrimmedJournals(); count > 0 {
		a.sessionLog("trimmed-sessions", fmt.Sprintf("count=%d", count))
	}
	left := restorePerStart
	if count := a.recoverJournalEntries(left); count > 0 {
		a.sessionLog("recovered-sessions", fmt.Sprintf("count=%d", count))
		left -= count
	}
	if left <= 0 {
		return
	}
	if count := a.recoverRecordingEntries(left); count > 0 {
		a.sessionLog("recovered-recordings", fmt.Sprintf("count=%d", count))
	}
}

// historyWatermark — раньше какого времени восстанавливать нечего.
func (a *App) historyWatermark() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.watermarkLocked()
}

// newerThan — попадает ли сессия за водяную отметку. Ноль в отметке значит
// «истории ещё не было и её не очищали»: тогда годится всё, в том числе запись
// с неизвестным временем.
func newerThan(at, watermark time.Time) bool {
	if watermark.IsZero() {
		return true
	}
	return at.After(watermark)
}

// sessionBegan — когда началась сессия: из события start, а если его нет (так
// писали журналы прежних версий) — из имени файла: оно и есть время начала в
// местной зоне.
func sessionBegan(path string, session journalSession) time.Time {
	if !session.Began.IsZero() {
		return session.Began
	}
	if at, err := time.ParseInLocation("20060102-150405", sessionID(path), time.Local); err == nil {
		return at
	}
	return time.Time{}
}

// recoveredJournal — журнал, слова которого ещё не в истории, и запись, которую
// из него надо завести.
type recoveredJournal struct {
	path  string
	entry Entry
}

// journalRecoveries собирает записи для журналов, которых в истории ещё нет.
//
// Журнал с меткой deleted, cleared или trimmed пропускается: это решения
// человека и предела истории, а не потеря. Новее водяной отметки — тоже
// условие: старые строки история всё равно не удержит.
func (a *App) journalRecoveries() []recoveredJournal {
	paths, err := filepath.Glob(filepath.Join(a.Dir, "sessions", "*.jsonl"))
	if err != nil {
		return nil
	}
	watermark := a.historyWatermark()
	recovered := []recoveredJournal{}
	for _, path := range paths {
		session := readJournal(path)
		if session.Deleted || session.Cleared || session.Trimmed {
			continue
		}
		at := sessionBegan(path, session)
		if !newerThan(at, watermark) {
			continue
		}
		audio := session.Audio
		if audio == "" {
			// Журналы прежней версии не знали пути: ищем запись по имени сессии.
			audio = a.recordingForSession(sessionID(path))
		}
		id := sessionID(path)
		if audio != "" {
			// id записи — имя её звука, а не журнала: они могут разойтись на
			// секунду, а окно зовёт удаление и повтор именно по id записи.
			id = entryID(audio, at)
		}
		if a.historyHas(id) {
			continue
		}
		text := strings.TrimSpace(session.Text)
		if session.Status == "silence" || session.Status == "cancelled" {
			// Тишина и отмена — не потеря: звук убран нарочно, и возвращать их
			// в историю незачем. Без этого каждая такая сессия всплывала бы
			// ошибкой «файл записи пропал» или чужими словами.
			continue
		}
		if session.Status == statusAutostop && text == "" {
			// Автостоп без слов — тоже не потеря: остановила тишина, слов нет,
			// звук убран (см. finishAutostopEmpty). С текстом — обычная запись.
			continue
		}
		if text == "" {
			if audio == "" {
				// Ни слов, ни звука: заводить нечего, пустая строка была бы мусором.
				continue
			}
			info, err := os.Stat(audio)
			if err != nil {
				// Звук уже убран (вычистка записей) — разбирать нечего.
				continue
			}
			if time.Since(info.ModTime()) > restoreAudioAge {
				// Без текста разбирать имеет смысл только свежую потерю:
				// недельный архив человек разберёт сам, если захочет.
				continue
			}
		}
		status := statusDone
		if text == "" {
			status = statusTranscribing
		}
		recovered = append(recovered, recoveredJournal{path: path, entry: Entry{
			ID:          id,
			At:          at,
			Text:        text,
			Seconds:     journalSeconds(session),
			Destination: "restored",
			Audio:       audio,
			Status:      status,
			Recovered:   true,
		}})
	}
	// Свежее — вперёд: при потолке за старт восстанавливать надо то, что
	// потеряно только что, а не самый старый хвост.
	sort.SliceStable(recovered, func(left, right int) bool {
		return recovered[left].entry.At.After(recovered[right].entry.At)
	})
	return recovered
}

// recoverJournalEntries кладёт найденное в историю и только ПОТОМ метит журналы.
// Возвращает, сколько записей добавлено; больше limit за запуск не берёт.
func (a *App) recoverJournalEntries(limit int) int {
	recovered := a.journalRecoveries()
	if len(recovered) > limit {
		recovered = recovered[:limit]
	}
	if len(recovered) == 0 {
		return 0
	}
	added := 0
	kept := map[string]bool{}
	a.mu.Lock()
	for _, item := range recovered {
		if a.addHistoryLocked(item.entry, false) {
			added++
		}
	}
	if added > 0 {
		a.touchHistoryLocked()
		for _, entry := range a.state.History {
			kept[entry.ID] = true
		}
	}
	a.mu.Unlock()
	if added == 0 {
		return 0
	}
	if err := a.saveHistory(); err != nil {
		// История не легла на диск — метку не ставим: следующий запуск повторит.
		return 0
	}
	for _, item := range recovered {
		if item.entry.Status == statusTranscribing || !kept[item.entry.ID] {
			// Разбор ещё впереди: журнал помечается, когда запись станет done
			// или error (recoverRecordings), — иначе метка соврала бы.
			continue
		}
		if !markRecovered(item.path, item.entry) {
			a.sessionLog("journal-mark-failed", "id="+item.entry.ID)
		}
	}
	return added
}

// pendingRecovery — файл записи, у которого ещё нет итога в истории.
type pendingRecovery struct {
	id    string
	audio string
	at    time.Time
}

// journalledAudio — звуки, о которых говорят живые журналы. По ним видно, что
// файл принадлежит законченному или оборванному разговору: свежесть файла тогда
// ничего не значит, и ждать минуту ему незачем. Вычеркнутые журналы не в счёт:
// их звук восстановлению не принадлежит.
func (a *App) journalledAudio() map[string]bool {
	paths, err := filepath.Glob(filepath.Join(a.Dir, "sessions", "*.jsonl"))
	if err != nil {
		return nil
	}
	owned := map[string]bool{}
	for _, path := range paths {
		session := readJournal(path)
		if session.Deleted || session.Cleared || session.Trimmed {
			continue
		}
		if session.Audio != "" {
			owned[strings.ToLower(filepath.Clean(session.Audio))] = true
		}
	}
	return owned
}

// pendingRecordings перечисляет записи, которые ждут итога: их нет в истории
// или их строка висит в transcribing. Свежие файлы без журнала не трогаются: это
// может быть идущая прямо сейчас запись, и её заголовок ещё не дописан.
func (a *App) pendingRecordings() []pendingRecovery {
	folder := filepath.Join(a.Media, "recordings")
	patterns := []string{"*.wav", "*.mp3", "*.m4a", "*.ogg", "*.opus", "*.flac"}
	current := a.audio.RecordedPath()
	owned := a.journalledAudio()
	discardedID, discardedAudio := a.journalDiscards()
	watermark := a.historyWatermark()
	pending := []pendingRecovery{}
	for _, pattern := range patterns {
		paths, err := filepath.Glob(filepath.Join(folder, pattern))
		if err != nil {
			continue
		}
		for _, path := range paths {
			if samePath(path, current) {
				continue
			}
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			if !owned[strings.ToLower(filepath.Clean(path))] && time.Since(info.ModTime()) < restoreAfter {
				continue
			}
			if time.Since(info.ModTime()) > restoreAudioAge {
				// Без текста повторный разбор имеет смысл только для свежей
				// потери; недельный архив человек разберёт сам, если захочет.
				continue
			}
			id := entryID(path, info.ModTime())
			if discardedID[id] || discardedAudio[strings.ToLower(filepath.Clean(path))] {
				// Человек удалил эту запись, очистил историю или её строку
				// вытеснил предел: звук мог остаться общим с другой строкой,
				// но возвращать её не надо.
				continue
			}
			if known, found := a.recoveredEntry(id); found {
				if known.Status != statusTranscribing {
					continue
				}
			} else if !newerThan(info.ModTime(), watermark) {
				// Старее водяной отметки: предел истории вытеснил бы строку
				// сразу, и разбирать её заново при каждом запуске незачем.
				continue
			}
			pending = append(pending, pendingRecovery{id: id, audio: path, at: info.ModTime()})
		}
	}
	// Свежее — вперёд: при потолке за старт восстанавливать надо то, что
	// потеряно только что, а не самый старый хвост.
	sort.SliceStable(pending, func(left, right int) bool {
		return pending[left].at.After(pending[right].at)
	})
	return pending
}

// recoverRecordingEntries заводит строки для записей, у которых их нет: текст у
// них взять неоткуда, поэтому строка сразу говорит «расшифровывается», а разбор
// идёт в фоне.
func (a *App) recoverRecordingEntries(limit int) int {
	pending := a.pendingRecordings()
	if len(pending) > limit {
		pending = pending[:limit]
	}
	if len(pending) == 0 {
		return 0
	}
	added := 0
	a.mu.Lock()
	for _, item := range pending {
		if _, found := a.findHistoryLocked(item.id); found {
			continue
		}
		if strings.EqualFold(filepath.Ext(item.audio), ".m4a") && a.brokenM4A(item.audio) {
			// Старый .m4a без оглавления сперва достаёт звук декодером; об этом
			// скажет recoverOldM4A, а не «расшифровывается» навсегда.
			continue
		}
		seconds, _ := recordingSeconds(item.audio)
		entry := historyEntry("", item.audio, "restored", seconds, statusTranscribing, "", true)
		entry.ID, entry.At = item.id, item.at
		if a.addHistoryLocked(entry, false) {
			added++
		}
	}
	if added > 0 {
		a.touchHistoryLocked()
	}
	a.mu.Unlock()
	if added == 0 {
		return 0
	}
	if err := a.saveHistory(); err != nil {
		// Строки в history.json нет: следующий запуск заведёт их заново.
		return 0
	}
	return added
}

// recoverInterrupted — восстановление после сбоя: старые .m4a без оглавления,
// записи без текста и строки, у которых пропал звук.
//
// Оно идёт ПОСЛЕ запуска, в фоне: разбор длится дольше, чем открывается окно и
// регистрируется сочетание клавиш, и человек не должен ждать чужую неудачу.
// Строки для этого уже заведены в NewApp (recoverAtStart): здесь появляется
// только итог — done или error.
func (a *App) recoverInterrupted() {
	a.recoverOldM4A()
	a.recoverRecordings()
}

// recoverOldM4A достаёт звук из записей прежней версии, у которых нет moov.
//
// Пакет aacrecover кладёт рядом WAV; дальше его подберёт обход записей, как
// обычную запись без журнала.
func (a *App) recoverOldM4A() {
	decoder := a.restoreDecoder
	if decoder == nil {
		ffmpeg, err := aacrecover.FindFFmpeg()
		if err != nil {
			// Без декодера не восстанавливаем молча: по журналу должно быть
			// видно, почему запись осталась нечитаемой.
			a.sessionLog("restore-refused", "reason="+err.Error())
			return
		}
		decoder = aacrecover.NewFFmpeg(ffmpeg)
	}
	outcomes, err := aacrecover.RecoverDir(
		filepath.Join(a.Media, "recordings"), decoder, restoreAfter)
	if err != nil {
		a.sessionLog("restore-failed", err.Error())
		return
	}
	for _, outcome := range outcomes {
		if outcome.Err != nil {
			a.sessionLog("restore-failed",
				"audio="+filepath.Base(outcome.Source)+" err="+outcome.Err.Error())
			continue
		}
		a.sessionLog("restored-audio", fmt.Sprintf(
			"source=%s audio=%s frames=%d seconds=%.1f",
			filepath.Base(outcome.Source), filepath.Base(outcome.Audio),
			outcome.Result.Frames, outcome.Result.Seconds))
		// Свежий WAV обход записей пропустит (он моложе минуты), поэтому
		// разбираем его сразу: иначе восстановленный звук ждал бы следующего
		// запуска службы.
		at := time.Now()
		if info, err := os.Stat(outcome.Source); err == nil {
			at = info.ModTime()
		}
		id := entryID(outcome.Audio, at)
		if known, found := a.recoveredEntry(id); found && known.Status != statusTranscribing {
			// Итог уже есть: второй раз тот же звук не разбираем.
			continue
		}
		if !newerThan(at, a.historyWatermark()) {
			// Строку всё равно вытеснил бы предел истории: разбирать незачем.
			continue
		}
		if time.Since(at) > restoreAudioAge {
			// Недельный архив без текста сами не разбираем: его можно
			// расшифровать вручную, когда он понадобится.
			continue
		}
		entry := a.restoreEntry(outcome.Audio, at)
		entry.ID = id
		if a.storeRestoredEntry(entry) {
			a.markJournalRecovered(entry.ID, entry)
			a.sessionLog("restored-recording", fmt.Sprintf(
				"id=%s audio=%s status=%s seconds=%.1f", entry.ID, filepath.Base(entry.Audio), entry.Status, entry.Seconds))
		}
	}
}

// recoverRecordings разбирает записи, которые ждут итога, и закрывает
// восстановленные строки, у которых пропал звук.
//
// Сюда попадают и разговоры, у которых в журнале не было ни слова: строка для
// них уже заведена в NewApp со статусом transcribing. Запись без журнала вовсе
// тоже здесь: сбой мог убить процесс раньше, чем открылся журнал.
func (a *App) recoverRecordings() {
	for _, item := range a.pendingRecordings() {
		if a.ctx.Err() != nil {
			return
		}
		if strings.EqualFold(filepath.Ext(item.audio), ".m4a") && a.brokenM4A(item.audio) {
			a.rememberBrokenRecording(item.audio, item.at)
			continue
		}
		entry := a.restoreEntry(item.audio, item.at)
		entry.ID = item.id
		if !a.storeRestoredEntry(entry) {
			continue
		}
		a.markJournalRecovered(item.id, entry)
		a.sessionLog("restored-recording", fmt.Sprintf(
			"id=%s audio=%s status=%s seconds=%.1f", entry.ID, filepath.Base(item.audio), entry.Status, entry.Seconds))
	}
	a.recoverLost()
}

// recoverLost закрывает восстановленные строки, которые больше не будут
// разобраны: без звука или слишком старые. Иначе они навсегда остались бы
// «расшифровывается».
func (a *App) recoverLost() {
	a.mu.Lock()
	changed := false
	for index := range a.state.History {
		entry := &a.state.History[index]
		if !entry.Recovered || entry.Status != statusTranscribing {
			continue
		}
		if entry.Audio != "" {
			if info, err := os.Stat(entry.Audio); err == nil {
				if time.Since(info.ModTime()) <= restoreAudioAge {
					// Звук свежий — его ещё разберут в этом запуске или следующем.
					continue
				}
				entry.Status, entry.Error = statusError,
					"звук старше недели: повторный разбор не делался, можно расшифровать вручную"
				changed = true
				continue
			}
		}
		entry.Status, entry.Error = statusError, "файл записи пропал"
		changed = true
	}
	if changed {
		a.touchHistoryLocked()
	}
	a.mu.Unlock()
	if changed {
		_ = a.saveHistory()
	}
}

// restoreEntry разбирает звук и готовит запись истории: удачный разбор — done,
// отказ — error с причиной. Звук остаётся на месте в обоих случаях.
//
// Длительность берётся из самого файла: у Ogg/Opus — по позиции последней целой
// страницы. Оборванная запись ничего не теряет: страницы самодостаточны, и файл
// как есть годен — чинить в нём нечего, в отличие от старого .m4a без moov.
func (a *App) restoreEntry(audio string, at time.Time) Entry {
	seconds, _ := recordingSeconds(audio)
	text, err := a.transcribeRestored(audio)
	entry := historyEntry(text, audio, "restored", seconds, statusDone, "", true)
	if err != nil || strings.TrimSpace(text) == "" {
		entry.Status = statusError
		if err != nil {
			// Причина — словами разбора, а не подписью облака: у подменного
			// разбора своя причина («нет сети»), и подменять её чужим видом
			// отказа значит прятать то, что сказал распознаватель.
			if text, ok := refusalText(err); ok {
				entry.Error = text
			} else {
				entry.Error = failureReason(err)
			}
		} else {
			entry.Error = "распознавание не удалось"
		}
	}
	entry.At = at
	return entry
}

// transcribeRestored зовёт распознавание так же, как обычная расшифровка, но
// умеет подставной разбор в проверках.
func (a *App) transcribeRestored(audio string) (string, error) {
	if _, err := os.Stat(audio); err != nil {
		return "", fmt.Errorf("%w: %v", errAudioNotFound, err)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	result, err := a.TranscribeFile(ctx, audio)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Text), nil
}

// storeRestoredEntry кладёт восстановленный итог в историю и пишет её на диск.
// Уже известная строка не заводится второй раз: у неё обновляется итог. false —
// история не записалась, и метку в журнале ставить нельзя.
func (a *App) storeRestoredEntry(entry Entry) bool {
	if entry.ID == "" {
		return false
	}
	a.mu.Lock()
	if index, found := a.findHistoryLocked(entry.ID); found {
		known := &a.state.History[index]
		known.Text, known.Status, known.Error = entry.Text, entry.Status, entry.Error
		known.Destination, known.Recovered = entry.Destination, true
		if entry.Seconds > 0 {
			known.Seconds = entry.Seconds
		}
		if entry.Audio != "" {
			known.Audio = entry.Audio
		}
	} else if !a.addHistoryLocked(entry, false) {
		a.mu.Unlock()
		return false
	}
	a.touchHistoryLocked()
	a.mu.Unlock()
	if err := a.saveHistory(); err != nil {
		a.sessionLog("history-write-failed", err.Error())
		return false
	}
	return true
}

// recoveredEntry возвращает строку истории по id.
func (a *App) recoveredEntry(id string) (Entry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if index, found := a.findHistoryLocked(id); found {
		return a.state.History[index], true
	}
	return Entry{}, false
}

// historyHas — есть ли уже запись с таким id.
func (a *App) historyHas(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, found := a.findHistoryLocked(id)
	return found
}

// findJournal находит журнал сессии: по звуку, о котором он говорит, или по
// имени сессии. Звук точнее — имя журнала и записи может разойтись на секунду.
func (a *App) findJournal(id, audio string) string {
	paths, err := filepath.Glob(filepath.Join(a.Dir, "sessions", "*.jsonl"))
	if err != nil {
		return ""
	}
	fallback := ""
	for _, path := range paths {
		session := readJournal(path)
		if audio != "" && samePath(session.Audio, audio) {
			return path
		}
		if fallback == "" && sessionID(path) == id {
			fallback = path
		}
	}
	return fallback
}

// journalDiscards — журналы, вычеркнутые человеком (deleted, cleared) или
// пределом истории (trimmed), по имени сессии и по пути звука. Один проход по
// журналам вместо поиска на каждую запись: на машине владельца их две сотни, а
// записей — двести тридцать три.
func (a *App) journalDiscards() (byID, byAudio map[string]bool) {
	byID, byAudio = map[string]bool{}, map[string]bool{}
	paths, err := filepath.Glob(filepath.Join(a.Dir, "sessions", "*.jsonl"))
	if err != nil {
		return byID, byAudio
	}
	for _, path := range paths {
		session := readJournal(path)
		if !session.Deleted && !session.Cleared && !session.Trimmed {
			continue
		}
		byID[sessionID(path)] = true
		if session.Audio != "" {
			byAudio[strings.ToLower(filepath.Clean(session.Audio))] = true
		}
	}
	return byID, byAudio
}

// markJournalTrimmed — метка вытесненной пределом строки.
func (a *App) markJournalTrimmed(id, audio string) {
	path := a.findJournal(id, audio)
	if path == "" {
		return
	}
	session := readJournal(path)
	if session.Deleted || session.Cleared || session.Trimmed {
		return
	}
	if !markTrimmed(path) {
		a.sessionLog("journal-mark-failed", "id="+id)
	}
}

// markTrimmedJournals помечает журналы, которые старше водяной отметки: их
// строки вытеснил предел истории, и восстановление не должно их возвращать.
// Метка переживает и потерю history.json — вытесненное не вернётся даже тогда,
// когда история стала короче.
func (a *App) markTrimmedJournals() int {
	watermark := a.historyWatermark()
	if watermark.IsZero() {
		// Истории ещё не было и её не очищали: вытеснять нечего.
		return 0
	}
	paths, err := filepath.Glob(filepath.Join(a.Dir, "sessions", "*.jsonl"))
	if err != nil {
		return 0
	}
	marked := 0
	for _, path := range paths {
		session := readJournal(path)
		if session.Deleted || session.Cleared || session.Trimmed {
			continue
		}
		at := sessionBegan(path, session)
		if at.IsZero() {
			// Времени нет ни в журнале, ни в имени: считаем старым — отметка
			// уже есть, значит история велась, а эта сессия в неё не попала.
			at = watermark.Add(-time.Second)
		}
		if !at.Before(watermark) {
			// Самая старая строка истории не вытеснена, и её журнал метить
			// нельзя: иначе dropDiscardedHistory уберёт живую строку.
			continue
		}
		if markTrimmed(path) {
			marked++
		}
	}
	return marked
}

// markJournalsCleared оставляет метку cleared во всех живых журналах: человек
// убрал историю целиком, и восстановление не должно вернуть её обратно.
func (a *App) markJournalsCleared() int {
	paths, err := filepath.Glob(filepath.Join(a.Dir, "sessions", "*.jsonl"))
	if err != nil {
		return 0
	}
	marked := 0
	for _, path := range paths {
		session := readJournal(path)
		if session.Deleted || session.Cleared || session.Trimmed {
			continue
		}
		if !session.Finished {
			// Разговор ещё идёт (или оборвался, не дописав итог): очистка
			// истории не должна выбрасывать его — строку заведёт обычный ход,
			// а не восстановление. Обрыв старше очистки отсечёт cleared_at.
			continue
		}
		if markCleared(path) {
			marked++
		}
	}
	return marked
}

// markJournalRecovered ставит метку recovered. Зовётся ТОЛЬКО после того, как
// итог уже лёг в history.json: обратный порядок и потерял запись 20261111-231748.
func (a *App) markJournalRecovered(id string, entry Entry) {
	path := a.findJournal(id, entry.Audio)
	if path == "" {
		return
	}
	if !markRecovered(path, entry) {
		a.sessionLog("journal-mark-failed", "id="+id)
	}
}

// markJournalDeleted оставляет в журнале метку удаления. Зовётся ДО записи
// истории: сбой между ними иначе вернул бы удалённую запись при следующем
// запуске.
func (a *App) markJournalDeleted(id, audio string) {
	path := a.findJournal(id, audio)
	if path == "" {
		return
	}
	if !markDeleted(path, audio) {
		a.sessionLog("journal-mark-failed", "id="+id)
	}
}

// dropDiscardedHistory убирает строки, удаление которых не доиграло: метка в
// журнале уже стоит, а history.json не успел. Человек удалил или очистил
// историю, предел вытеснил строку — возвращать её нельзя.
func (a *App) dropDiscardedHistory() {
	a.mu.Lock()
	history := append([]Entry(nil), a.state.History...)
	a.mu.Unlock()
	discardedID, discardedAudio := a.journalDiscards()
	kept := make([]Entry, 0, len(history))
	for _, entry := range history {
		if discardedID[entry.ID] {
			continue
		}
		if entry.Audio != "" && discardedAudio[strings.ToLower(filepath.Clean(entry.Audio))] {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) == len(history) {
		return
	}
	a.mu.Lock()
	a.state.History = kept
	a.touchHistoryLocked()
	a.mu.Unlock()
	if err := a.saveHistory(); err != nil {
		a.sessionLog("history-write-failed", err.Error())
	}
}

// recordingForSession ищет запись по имени сессии: так находят друг друга
// журнал и звук без общего поля.
//
// Порядок расширений не случаен: у одной сессии могут лежать и старый .m4a, и
// восстановленный из него .wav, и разбирать надо читаемое. WAV первым ещё и
// потому, что это то, что пишут новые записи.
func (a *App) recordingForSession(id string) string {
	matches, err := filepath.Glob(filepath.Join(a.Media, "recordings", id+".*"))
	if err != nil {
		return ""
	}
	priority := []string{".wav", ".mp3", ".m4a", ".ogg", ".opus", ".flac"}
	for _, extension := range priority {
		for _, match := range matches {
			if strings.EqualFold(filepath.Ext(match), extension) {
				return match
			}
		}
	}
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// brokenM4A — старая запись без оглавления, из которой звук так и не достали.
func (a *App) brokenM4A(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return !aacrecover.HasIndex(data)
}

// rememberBrokenRecording кладёт в историю честную ошибку вместо молчания:
// запись не теряется из виду, и причина названа.
func (a *App) rememberBrokenRecording(path string, at time.Time) {
	entry := historyEntry("", path, "restored", 0, statusError,
		"файл повреждён: нет оглавления, звук не восстановлен", true)
	entry.At = at
	if a.storeRestoredEntry(entry) {
		a.sessionLog("restore-failed", "audio="+filepath.Base(path)+" reason=no-moov")
	}
}
