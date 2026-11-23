package localdict

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Срок хранения звука — настройка «Удалять звук старше». Текст записи при этом
// остаётся в истории: удаляется только звук, как у ручной «Подчистить» со
// строкой, оставленной на месте.
//
// ЗАЧЕМ ОТДЕЛЬНО ОТ «Подчистить». Ручная очистка убирает строки целиком, а
// автоочистке нужна обратная вещь: текст остаётся, звука нет. Общим у них
// остаётся удаление звука — dropAudio из history_actions.go.

// defaultAudioKeepDays — срок по умолчанию: месяц. Ноль в настройке значит
// «не удалять по возрасту».
const defaultAudioKeepDays = 30

// audioProtectDays — сколько звук строк с ошибкой, в ожидании или в разборе
// живёт, даже если срок настройки уже вышел. Такой звук нужен для повтора, и
// повтор не должен пропасть из-за того, что человек поставил «через день».
const audioProtectDays = 90

// storageStatTTL — как долго держится посчитанный объём папки записей. Окно
// читает настройки при заходе, а не на каждый кадр; пересчёт дешёвый, но диск
// не нужно трогать по каждому запросу.
const storageStatTTL = time.Minute

// audioKeepChoices — допустимые сроки. Окно предлагает именно эти значения,
// поэтому проверка здесь и в validateConfig держит один и тот же список.
var audioKeepChoices = []int{0, 1, 7, 14, 30, 90}

// validAudioKeepDays — входит ли срок в список допустимых.
func validAudioKeepDays(days int) bool {
	for _, choice := range audioKeepChoices {
		if days == choice {
			return true
		}
	}
	return false
}

// audioDue — пора ли удалить звук этой строки.
//
// Готовые строки (done, autostop) отдаются сроку настройки. Строки с ошибкой
// держат звук до audioProtectDays: по нему можно повторить разбор, и удалять его
// по сроку «через день» значит лишить человека повтора.
//
// Строки в ожидании повтора (retrying) и в разборе (transcribing) не попадают
// под уборку никогда. Их звук нужен очереди прямо сейчас. Уборка, убрав звук
// у retrying, оставила бы строку без файла: nextDue берёт первую due-строку и,
// не найдя у неё звука, перестаёт обходить очередь до перезапуска. Для
// transcribing причина та же: разбор идёт, его заберёт восстановление, либо
// «Расшифровать заново» только что поставило строку в разбор.
func audioDue(entry Entry, cutoff, protected time.Time) bool {
	if entry.At.IsZero() {
		return false
	}
	switch entry.Status {
	case statusDone, statusAutostop:
		return entry.At.Before(cutoff)
	case statusError:
		return entry.At.Before(protected)
	}
	return false
}

// errRecoveryRunning — отказ удалять звук, пока восстановление не закончилось.
// Текст показывает окно как есть, поэтому он на русском и без технических слов.
var errRecoveryRunning = errors.New("Идёт восстановление записей, это может занять несколько минут — повторите позже")

// recoveryGate — можно ли сейчас удалять звук. Восстановление собирает записи
// прерванной диктовки и может забрать звук строк в разборе; очистка, которая
// пришла раньше, удалила бы его. Порядок «сперва восстановление, потом уборка»
// обязателен (см. storageLoop), и любой вход в удаление звука ходит через него.
func (a *App) recoveryGate() error {
	a.mu.Lock()
	done := a.recoveryDone
	a.mu.Unlock()
	if !done {
		return errRecoveryRunning
	}
	return nil
}

// autocleanAudio — автоочистка по сроку из настроек. Зовётся уборкой при старте
// и раз в сутки, после восстановления и после срока взведения уборки (см.
// cleanupStorage). Ноль в настройке — ничего не делает.
func (a *App) autocleanAudio(now time.Time) (rows int, freed int64) {
	a.mu.Lock()
	days := a.state.Config.AudioKeepDays
	a.mu.Unlock()
	if days <= 0 {
		return 0, 0
	}
	rows, freed = a.clearAudioOlderThan(now, days)
	a.sessionLog("audio-autoclean", "removed="+itoa(rows)+" freed_bytes="+itoa64(freed)+" days="+itoa(days))
	return rows, freed
}

// clearAudioOlderThan убирает звук строк старше days дней; строки и их текст
// остаются. Возвращает число строк, у которых звук убран, и байты на диске.
//
// Порядок тот же, что у ручной очистки: сперва история на диске, потом файлы.
// Сбой между ними оставит лишний файл, который уберёт уборка сирот; обратный
// порядок оставил бы строку, указывающую на уже удалённый звук.
func (a *App) clearAudioOlderThan(now time.Time, days int) (rows int, freed int64) {
	cutoff := now.AddDate(0, 0, -days)
	protected := now.AddDate(0, 0, -audioProtectDays)
	// Звук пишущейся записи ещё не в истории, но строка может указывать на тот
	// же файл (повтор старой записи поверх нового). Его не трогаем.
	current := audioKey(a.audio.RecordedPath())
	var chosen, kept []Entry
	a.mu.Lock()
	for index := range a.state.History {
		entry := a.state.History[index]
		if entry.Audio != "" && audioKey(entry.Audio) != current && audioDue(entry, cutoff, protected) {
			// Копия с путём уходит в chosen до того, как строка очищена: dropAudio
			// должен знать, какой файл убирать.
			chosen = append(chosen, entry)
			a.state.History[index].Audio = ""
			continue
		}
		kept = append(kept, entry)
	}
	if len(chosen) == 0 {
		a.mu.Unlock()
		return 0, 0
	}
	a.touchHistoryLocked()
	err := a.saveHistoryLocked()
	a.mu.Unlock()
	if err != nil {
		// Историю не записали — звук трогать нельзя: на диске строка всё ещё
		// ссылается на него.
		a.sessionLog("audio-autoclean-failed", err.Error())
		return 0, 0
	}
	_, freed = a.dropAudio(chosen, kept)
	a.invalidateRecordingsStat()
	return len(chosen), freed
}

// recordingsStat — что занимают записи на диске.
type recordingsStat struct {
	bytes int64
	count int
	// oldest — дата самого старого файла записи; нулевая, если записей нет.
	oldest time.Time
}

// StorageView — объём записей для окна. Дата старейшей — пустая, если записей нет.
type StorageView struct {
	RecordingsBytes int64      `json:"recordings_bytes"`
	RecordingsCount int        `json:"recordings_count"`
	OldestAt        *time.Time `json:"oldest_at,omitempty"`
}

// recordingsStats отдаёт объём папки записей, пересчитывая его не чаще
// storageStatTTL. Пересчёт идёт под замком кэша: два окна, открытых сразу, не
// запускают два обхода папки.
func (a *App) recordingsStats() recordingsStat {
	a.storageMu.Lock()
	defer a.storageMu.Unlock()
	if !a.storageAt.IsZero() && time.Since(a.storageAt) < storageStatTTL {
		return a.storageCache
	}
	a.storageCache = scanRecordings(filepath.Join(a.Media, "recordings"))
	a.storageAt = time.Now()
	return a.storageCache
}

// invalidateRecordingsStat сбрасывает кэш: после уборки объём уже другой, и
// окно не должно показывать прежнее число до истечения срока.
func (a *App) invalidateRecordingsStat() {
	a.storageMu.Lock()
	a.storageAt = time.Time{}
	a.storageMu.Unlock()
}

// scanRecordings считает звуковые файлы папки. Не-звуковые файлы и подпапки
// не учитываются: окно говорит о записях, а не обо всём, что лежит рядом.
func scanRecordings(dir string) recordingsStat {
	var stat recordingsStat
	entries, err := os.ReadDir(dir)
	if err != nil {
		return stat
	}
	for _, entry := range entries {
		if entry.IsDir() || !audioExtension(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		stat.bytes += info.Size()
		stat.count++
		if stat.oldest.IsZero() || info.ModTime().Before(stat.oldest) {
			stat.oldest = info.ModTime()
		}
	}
	return stat
}

// storageView — объём для окна настроек.
func (a *App) storageView() StorageView {
	stat := a.recordingsStats()
	view := StorageView{RecordingsBytes: stat.bytes, RecordingsCount: stat.count}
	if !stat.oldest.IsZero() {
		oldest := stat.oldest
		view.OldestAt = &oldest
	}
	return view
}

// kickAutoclean — внеочередная уборка сразу после смены срока человеком.
//
// Пока восстановление не прошло, звук не трогаем: порядок «сперва восстановление,
// потом уборка» обязателен (см. storageLoop). Флаг выбора уже записан в
// config.json, поэтому первый проход уборки после восстановления всё равно
// учтёт срок.
func (a *App) kickAutoclean() {
	a.mu.Lock()
	recovered := a.recoveryDone
	a.mu.Unlock()
	if !recovered {
		return
	}
	a.autocleanAudio(time.Now())
}

// markAudioKeepChosen запоминает, что срок выбрал человек: «Очистить сейчас»
// — такой же выбор, как смена срока, и задержку взведения он снимает.
func (a *App) markAudioKeepChosen() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Config.AudioKeepDaysSet {
		return
	}
	a.state.Config.AudioKeepDaysSet = true
	if err := a.writeState("config.json", a.state.Config); err != nil {
		a.sessionLog("audio-keep-flag-failed", err.Error())
	}
}

// audioKeepRoutes — POST /storage/audio-clean: «Очистить сейчас» в настройках.
// Идёт тем же ядром, что и автоочистка, только сразу и по сроку из настроек.
// Срок «никогда» здесь ничего не делает: кнопку в окне для него не показывают,
// а сервер всё равно не удаляет без срока.
func (a *App) audioKeepRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /storage/audio-clean", func(w http.ResponseWriter, r *http.Request) {
		if err := a.recoveryGate(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		a.markAudioKeepChosen()
		rows, freed := a.autocleanAudio(time.Now())
		jsonResponse(w, map[string]any{"removed": rows, "freed_bytes": freed})
	})
}
