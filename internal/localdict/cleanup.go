package localdict

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/appdirs"
)

const (
	// recordingsVolumeLimit — сколько всего занимает папка записей. Диктовка
	// пишет сжатый Ogg/Opus (около восемнадцати мегабайт в час), но старые
	// сборки оставили WAV и m4a, а чужие программы присылают записи как есть:
	// без общего предела папка однажды занимает весь диск.
	recordingsVolumeLimit = int64(1) << 30
	// orphanLifetime — сколько файл без строки истории должен пролежать, прежде
	// чем его можно считать сиротой. Тридцать дней: на машине владельца в
	// recordings лежат сотни записей, которых история уже не помнит, и среди них
	// спасённая 20261111-231748. Свежая потеря важнее места, а старый архив
	// человек успеет разобрать сам.
	orphanLifetime = 30 * 24 * time.Hour
	// sweepArmDelay — сколько ждать после первого запуска на этой версии, прежде
	// чем что-то удалять. Первый запуск только считает и пишет метку: обновление
	// не должно стирать записи в день установки.
	sweepArmDelay = 30 * 24 * time.Hour
	// storageCleanupEvery — как часто уборка повторяется после запуска.
	storageCleanupEvery = 24 * time.Hour
)

// storageLoop — восстановление после сбоя, первая уборка и дальше уборка раз в
// сутки.
//
// Порядок явный и обязательный: сперва recoverInterrupted возвращает записи в
// историю, и только потом уборка считает, что связано со строкой, а что сирота.
// Обратный порядок удалил бы звук, который восстановление как раз собиралось
// разобрать; поэтому уборка не начинается ни по таймеру, ни параллельно.
func (a *App) storageLoop(ctx context.Context) {
	a.startupStorage(ctx)
	ticker := time.NewTicker(storageCleanupEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.cleanupStorage()
		}
	}
}

// startupStorage — проход восстановления и следующая за ним уборка.
//
// Вынесено из storageLoop, чтобы порядок можно было проверить без ожидания
// суточного такта: проверка зовёт startupStorage и видит, что уборка пошла
// только после восстановления.
func (a *App) startupStorage(ctx context.Context) {
	a.recoverInterrupted()
	if ctx.Err() != nil {
		// Службу останавливают: восстановление не доиграло, и удалять что-либо
		// на половине прохода нельзя.
		return
	}
	a.mu.Lock()
	a.recoveryDone = true
	a.mu.Unlock()
	a.cleanupStorage()
}

// cleanupStorage убирает всё, что не связано со строкой истории, и держит папку
// записей в пределах общего объёма.
//
// До первого «взведения» уборки и первые sweepArmDelay после него не удаляется
// НИЧЕГО: только считается и пишется в журнал. Так обновление на эту версию не
// стирает старые записи владельца, которых история уже не помнит.
func (a *App) cleanupStorage() {
	a.mu.Lock()
	recovered, armedAt, clearedAt := a.recoveryDone, a.sweepArmedAt, a.clearedAt
	a.mu.Unlock()
	if !recovered {
		a.sessionLog("storage-cleanup-skipped", "reason=recovery-not-finished")
		return
	}
	now := time.Now()
	// Срок, который выбрал сам человек, действует сразу: он ждёт результата, а
	// задержка взведения нужна только умолчанию, которого никто не трогал.
	a.mu.Lock()
	chosen := a.state.Config.AudioKeepDaysSet
	a.mu.Unlock()
	if chosen {
		a.autocleanAudio(now)
	}
	if armedAt.IsZero() {
		counted := a.countStorage()
		a.mu.Lock()
		a.sweepArmedAt = now
		a.mu.Unlock()
		if err := a.writeState(historyStateFile, historyState{ClearedAt: clearedAt, SweepArmedAt: now}); err != nil {
			// Метка не легла — удалять нельзя: иначе следующий запуск снова
			// сочтёт себя первым и не повторит счёт.
			a.sessionLog("storage-sweep-arm-failed", err.Error())
			return
		}
		a.sessionLog("storage-sweep-armed", counted.logLine())
		return
	}
	if now.Before(armedAt.Add(sweepArmDelay)) {
		// Срок ещё не вышел: ничего не удаляем, даже по пределу объёма.
		return
	}
	// Автоочистка по сроку из настроек идёт ДО подсчёта ссылок: звук, который
	// она убрала, у строк уже пустой, и сироты его не сочтут живым. Стоит под
	// тем же сроком взведения: обновление не стирает записи в день установки.
	// Предел в 200 файлов (pruneRecordings) и предел объёма в 1 ГиБ
	// (enforceRecordingsVolume) остаются страховкой поверх срока и не отменяются.
	if !chosen {
		a.autocleanAudio(now)
	}
	referenced := a.referencedAudio()
	recordings, freed := a.cleanupRecordings(referenced)
	phrases := a.cleanupPhrases(referenced)
	journals := a.cleanupJournals()
	temporary := a.cleanupTemp()
	evicted := a.enforceRecordingsVolume()
	if recordings+phrases+journals+temporary+evicted > 0 {
		a.sessionLog("storage-cleanup", "recordings="+itoa(recordings)+
			" phrases="+itoa(phrases)+" journals="+itoa(journals)+
			" temp="+itoa(temporary)+" over-limit="+itoa(evicted)+
			" bytes="+itoa64(freed))
	}
}

// storageCount — что уборка нашла на первом проходе. Считается, но не
// удаляется: по журналу видно, сколько записей владелец может потерять, если
// уборка когда-нибудь всё-таки сработает.
type storageCount struct {
	recordings, phrases, journals, temp int
	bytes, recordingsBytes              int64
}

func (c storageCount) logLine() string {
	return "recordings_orphans=" + itoa(c.recordings) +
		" phrases=" + itoa(c.phrases) +
		" journals=" + itoa(c.journals) +
		" temp=" + itoa(c.temp) +
		" orphan_bytes=" + itoa64(c.bytes) +
		" recordings_bytes=" + itoa64(c.recordingsBytes)
}

// countStorage считает сирот, ничего не трогая.
func (a *App) countStorage() storageCount {
	referenced := a.referencedAudio()
	counted := storageCount{}
	dir := filepath.Join(a.Media, "recordings")
	if entries, err := os.ReadDir(dir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !audioExtension(entry.Name()) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			counted.recordingsBytes += info.Size()
			if referenced[audioKey(filepath.Join(dir, entry.Name()))] {
				continue
			}
			counted.recordings++
			counted.bytes += info.Size()
		}
	}
	if paths, err := filepath.Glob(filepath.Join(a.Dir, "phrase-*")); err == nil {
		for _, path := range paths {
			if referenced[audioKey(path)] {
				continue
			}
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				counted.phrases++
				counted.bytes += info.Size()
			}
		}
	}
	counted.journals = len(a.orphanJournals())
	if root := a.tempFolder(); root != "" {
		if paths, err := filepath.Glob(filepath.Join(root, "dictation-*")); err == nil {
			for _, path := range paths {
				if info, err := os.Stat(path); err == nil && !info.IsDir() {
					counted.temp++
					counted.bytes += info.Size()
				}
			}
		}
	}
	return counted
}

// referencedAudio — звуки, которые нельзя трогать: они стоят у строк истории,
// принадлежат живым журналам или пишутся прямо сейчас.
//
// Звук пишущейся записи ещё не в истории, и без этой оговорки уборка, совпав с
// началом разговора, удалила бы его файл.
func (a *App) referencedAudio() map[string]bool {
	referenced := map[string]bool{}
	a.mu.Lock()
	for _, entry := range a.state.History {
		if entry.Audio != "" {
			referenced[audioKey(entry.Audio)] = true
		}
	}
	a.mu.Unlock()
	for path := range a.journalledAudio() {
		referenced[audioKey(path)] = true
	}
	if current := a.audio.RecordedPath(); current != "" {
		referenced[audioKey(current)] = true
	}
	return referenced
}

// cleanupRecordings убирает в recordings файлы, на которые не ссылается ни одна
// строка истории и которые пролежали дольше orphanLifetime.
//
// Свежие сироты остаются: файл мог только что появиться у разбора, его могла
// ещё не забрать история, и восстановление как раз ищет такие файлы.
func (a *App) cleanupRecordings(referenced map[string]bool) (removed int, freed int64) {
	dir := filepath.Join(a.Media, "recordings")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	now := time.Now()
	age := a.orphanAge()
	for _, entry := range entries {
		if entry.IsDir() || !audioExtension(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if referenced[audioKey(path)] {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < age {
			continue
		}
		if os.Remove(path) == nil {
			removed++
			freed += info.Size()
		}
	}
	return removed, freed
}

// cleanupPhrases убирает куски фраз, оставшиеся без строки истории: разбор
// успел сжать кусок, а запись не дошла. Живые и более свежие, чем
// orphanLifetime, не трогаются — их ещё может забрать восстановление.
func (a *App) cleanupPhrases(referenced map[string]bool) int {
	paths, err := filepath.Glob(filepath.Join(a.Dir, "phrase-*"))
	if err != nil {
		return 0
	}
	now := time.Now()
	age := a.orphanAge()
	removed := 0
	for _, path := range paths {
		if referenced[audioKey(path)] {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || now.Sub(info.ModTime()) < age {
			continue
		}
		if os.Remove(path) == nil {
			removed++
		}
	}
	return removed
}

// orphanJournals — журналы без строки истории. Используются и уборкой, и
// первым счётом: журнал нужен восстановлению и разбору, но без строки он только
// занимает место.
func (a *App) orphanJournals() []string {
	paths, err := filepath.Glob(filepath.Join(a.Dir, "sessions", "*.jsonl"))
	if err != nil {
		return nil
	}
	known := map[string]bool{}
	a.mu.Lock()
	for _, entry := range a.state.History {
		if entry.ID != "" {
			known[entry.ID] = true
		}
	}
	a.mu.Unlock()
	orphans := make([]string, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		session := readJournal(path)
		if known[sessionID(path)] || known[entryID(session.Audio, info.ModTime())] {
			continue
		}
		orphans = append(orphans, path)
	}
	return orphans
}

// cleanupJournals убирает журналы без строки истории, пролежавшие дольше
// orphanLifetime. Раньше этого срока журнал может понадобиться восстановлению.
func (a *App) cleanupJournals() int {
	now := time.Now()
	age := a.orphanAge()
	removed := 0
	for _, path := range a.orphanJournals() {
		info, err := os.Stat(path)
		if err != nil || now.Sub(info.ModTime()) < age {
			continue
		}
		if os.Remove(path) == nil {
			removed++
		}
	}
	return removed
}

// cleanupTemp убирает брошенные временные файлы разбора: /transcribe пишет
// запись во временную папку и удаляет её сам, но падение процесса оставляло бы
// её навсегда. Срок тот же — тридцать дней: резать раньше, чем разобрана
// свежая потеря, нельзя.
func (a *App) cleanupTemp() int {
	root := a.tempFolder()
	if root == "" {
		return 0
	}
	paths, err := filepath.Glob(filepath.Join(root, "dictation-*"))
	if err != nil {
		return 0
	}
	now := time.Now()
	age := a.orphanAge()
	removed := 0
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || now.Sub(info.ModTime()) < age {
			continue
		}
		if os.Remove(path) == nil {
			removed++
		}
	}
	return removed
}

// tempFolder — где искать брошенные временные файлы.
func (a *App) tempFolder() string {
	if strings.TrimSpace(a.tempDir) != "" {
		return a.tempDir
	}
	return appdirs.TempRoot()
}

// orphanAge — сколько файл должен пролежать без строки истории.
func (a *App) orphanAge() time.Duration {
	if a.orphanMinAge > 0 {
		return a.orphanMinAge
	}
	return orphanLifetime
}

// enforceRecordingsVolume держит папку записей в пределе общего объёма.
//
// Убираются самые старые ГОТОВЫЕ записи: у строки уже есть текст, и звук ей
// нужен только для прослушивания. Неудачные и ждущие повтора не трогаются —
// их звук и есть то, что можно разобрать ещё раз. Сироты этим пределом не
// затрагиваются: у них свой срок и свой проход.
func (a *App) enforceRecordingsVolume() int {
	dir := filepath.Join(a.Media, "recordings")
	limit := a.recordingVolumeLimit()
	total := folderBytes(dir)
	if total <= limit {
		return 0
	}
	type victim struct {
		index int
		at    time.Time
		audio string
	}
	shared := map[string]int{}
	a.mu.Lock()
	for _, entry := range a.state.History {
		if entry.Audio != "" {
			shared[audioKey(entry.Audio)]++
		}
	}
	var victims []victim
	for index := range a.state.History {
		entry := &a.state.History[index]
		if entry.Status != statusDone || entry.Audio == "" {
			continue
		}
		if shared[audioKey(entry.Audio)] > 1 {
			// Тот же звук стоит у другой строки: убрав его, мы сломали бы её.
			continue
		}
		if _, err := os.Stat(entry.Audio); err != nil {
			continue
		}
		victims = append(victims, victim{index: index, at: entry.At, audio: entry.Audio})
	}
	sort.SliceStable(victims, func(left, right int) bool {
		return victims[left].at.Before(victims[right].at)
	})
	evicted := 0
	removedAudio := make([]string, 0, len(victims))
	for _, item := range victims {
		if total <= limit {
			break
		}
		info, err := os.Stat(item.audio)
		if err != nil {
			continue
		}
		a.state.History[item.index].Audio = ""
		total -= info.Size()
		removedAudio = append(removedAudio, item.audio)
		evicted++
	}
	if evicted > 0 {
		a.touchHistoryLocked()
	}
	a.mu.Unlock()
	if evicted == 0 {
		return 0
	}
	// Файлы удаляются вне замка: история уже записана, и окно не ждёт диска.
	for _, path := range removedAudio {
		_ = os.Remove(path)
	}
	a.saveHistory()
	a.sessionLog("recordings-over-limit", "evicted="+itoa(evicted)+" limit="+itoa64(limit))
	return evicted
}

// recordingVolumeLimit — предел объёма записей: обычный или подставленный
// проверкой.
func (a *App) recordingVolumeLimit() int64 {
	if a.recordingsVolume > 0 {
		return a.recordingsVolume
	}
	return recordingsVolumeLimit
}

// folderBytes — сколько занимает папка.
func folderBytes(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}

// audioExtension — звуковой ли это файл по расширению.
func audioExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".wav", ".mp3", ".m4a", ".ogg", ".opus", ".flac", ".aac", ".webm":
		return true
	}
	return false
}

// itoa и itoa64 — числа в строку журнала.
func itoa(value int) string { return strconv.Itoa(value) }

func itoa64(value int64) string { return strconv.FormatInt(value, 10) }
