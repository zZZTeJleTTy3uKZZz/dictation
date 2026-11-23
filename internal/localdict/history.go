package localdict

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/opusrec"
)

// Статусы строки истории — один набор на всю службу.
//
// retrying — ждёт автоповтора (его время в NextRetryAt), transcribing — разбор
// идёт прямо сейчас, error — не вышло (причина в Error), done — обычная запись,
// autostop — запись остановлена сама: пять минут без голоса (см. idle.go).
// autostop с текстом — обычная запись, которую остановила тишина; без текста
// это не ошибка, и причина в Error только объясняет остановку.
const (
	statusDone         = "done"
	statusError        = "error"
	statusTranscribing = "transcribing"
	statusRetrying     = "retrying"
	statusAutostop     = "autostop"
)

// normalizeHistory приводит записи, написанные прежними версиями, к нынешнему
// виду: у старых нет id и status, и окно показало бы их сломанными.
//
// Статусы старых веток тоже читаются: unsent (ждала повтора) становится
// retrying, failed — error. Оборванный разбор (transcribing) разбирает
// restoreUnsent: ему нужен выбранный способ распознавания.
func normalizeHistory(history []Entry) []Entry {
	for index := range history {
		entry := &history[index]
		switch entry.Status {
		case "":
			entry.Status = statusDone
		case "unsent":
			entry.Status = statusRetrying
			if entry.NextRetryAt.IsZero() {
				entry.NextRetryAt = time.Now()
			}
		case "failed":
			entry.Status = statusError
			entry.NextRetryAt = time.Time{}
			if entry.Error == "" {
				entry.Error = "фраза не распознана"
			}
		}
		if entry.ID == "" {
			if entry.At.IsZero() {
				entry.ID = fmt.Sprintf("entry-%d", index)
			} else {
				entry.ID = entry.At.Format("20060102-150405")
			}
		}
		// Прежнее восстановление помечало себя только destination: поле
		// recovered появилось позже, и старые записи надо прочитать так же.
		if entry.Destination == "restored" {
			entry.Recovered = true
		}
		if entry.Seconds < 0 {
			entry.Seconds = 0
		}
	}
	return history
}

// entryID — устойчивое имя записи по файлу звука: имя файла без расширения.
// Оно же имя сессии в журнале (20261111-193914), поэтому запись, звук и журнал
// находят друг друга.
func entryID(audio string, at time.Time) string {
	if audio != "" {
		return strings.TrimSuffix(filepath.Base(audio), filepath.Ext(audio))
	}
	return at.Format("20060102-150405")
}

// journalStatus переводит итог разговора в статус журнала.
//
// У истории статусов несколько (done, error, transcribing, autostop), а у
// разговора бывает ещё тишина и отмена: в журнале они остаются своими словами,
// чтобы по нему было видно, что именно случилось. Автостоп без текста — отдельный
// статус: восстановление его не возвращает (как тишину), а с текстом это обычная
// запись, и она уходит в журнал как done.
func journalStatus(stage string, err error, text string) string {
	if err != nil || stage == "error" {
		return "error"
	}
	if stage == "silence" || stage == "cancelled" {
		return stage
	}
	if stage == "autostop" && strings.TrimSpace(text) == "" {
		return statusAutostop
	}
	return "done"
}

// historyEntry готовит запись истории: id по звуку, статус по итогу.
func historyEntry(text, audio, destination string, seconds float64, status, reason string, recovered bool) Entry {
	at := time.Now()
	return Entry{
		ID:          entryID(audio, at),
		At:          at,
		Text:        strings.TrimSpace(text),
		Seconds:     math.Max(0, seconds),
		Destination: destination,
		Audio:       audio,
		Status:      status,
		Error:       reason,
		Recovered:   recovered,
	}
}

// addHistoryLocked ставит запись в начало истории, не повторяя уже известную.
// Замок a.mu держит вызывающий: история пишется на диск под ним.
func (a *App) addHistoryLocked(entry Entry, front bool) bool {
	for _, known := range a.state.History {
		if known.ID == entry.ID && entry.ID != "" {
			return false
		}
	}
	if front {
		a.state.History = a.trimHistoryLocked(append([]Entry{entry}, a.state.History...))
	} else {
		// Порядок обязателен: сперва поставить на место, потом резать. Прежде
		// предел резал конец СПИСКА ДО сортировки, и новая запись, вставшая в
		// конец, вылетала первой — история хранила не последние сто, а первые.
		a.state.History = append(a.state.History, entry)
		sort.SliceStable(a.state.History, func(left, right int) bool {
			return a.state.History[left].At.After(a.state.History[right].At)
		})
		a.state.History = a.trimHistoryLocked(a.state.History)
	}
	return true
}

// saveHistoryLocked пишет историю на диск. Зовётся под a.mu: снимок уже снят,
// а порядок версий тот же, что у saveHistory, — старая запись не затрёт новую.
// Ошибка не роняет службу: история останется в памяти до следующей записи.
func (a *App) saveHistoryLocked() error {
	a.touchHistoryLocked()
	return a.writeHistory(a.state.HistoryRev, append([]Entry(nil), a.state.History...))
}

// historyStateFile — отметка об очистке истории. Отдельным файлом, а не полем
// в history.json: файл истории — это массив строк, и его читают и прежние
// версии, и окно; смена формы сломала бы обе.
const historyStateFile = "history-state.json"

// historyState — что помнит история сверх самих строк.
type historyState struct {
	// ClearedAt — когда человек очистил историю целиком. Всё, что записано
	// раньше, восстановление не возвращает.
	ClearedAt time.Time `json:"cleared_at,omitempty"`
	// SweepArmedAt — когда уборка впервые увидела файлы на этой версии.
	// Первый запуск после обновления ничего не удаляет: у владельца в
	// recordings могут лежать сотни записей, которых история уже не помнит, и
	// стирать их в день обновления нельзя. Удаление — не раньше чем через
	// sweepArmDelay после этой метки.
	SweepArmedAt time.Time `json:"sweep_armed_at,omitempty"`
}

// watermarkLocked — с какого времени восстановление имеет смысл: сессии новее
// самой старой строки истории (более старые вытеснил бы её предел) или, если
// история пуста, новее очистки. Ноль — восстанавливать можно всё: истории ещё
// не было и её не очищали. Зовётся под a.mu.
func (a *App) watermarkLocked() time.Time {
	watermark := a.clearedAt
	oldest := time.Time{}
	for _, entry := range a.state.History {
		// Строка без времени границу не задаёт: ноль тянул бы отметку в
		// начало времён и открывал восстановление всему подряд.
		if entry.At.IsZero() {
			continue
		}
		if oldest.IsZero() || entry.At.Before(oldest) {
			oldest = entry.At
		}
	}
	if oldest.After(watermark) {
		watermark = oldest
	}
	return watermark
}

// trimHistoryLocked режет историю пределом и запоминает, что именно вытеснено:
// вытесненным нельзя вернуться из журнала, и метка trimmed ставится после
// записи истории (диск под замком не держим). Зовётся под a.mu.
func (a *App) trimHistoryLocked(history []Entry) []Entry {
	trimmed := trimHistory(history)
	if len(trimmed) == len(history) {
		return trimmed
	}
	a.trimMu.Lock()
	if a.trimmedPending == nil {
		a.trimmedPending = map[string]string{}
	}
	for _, entry := range history[len(trimmed):] {
		if entry.ID != "" {
			a.trimmedPending[entry.ID] = entry.Audio
		}
	}
	a.trimMu.Unlock()
	return trimmed
}

// findHistoryLocked ищет запись по id.
func (a *App) findHistoryLocked(id string) (int, bool) {
	for index := range a.state.History {
		if a.state.History[index].ID == id {
			return index, true
		}
	}
	return 0, false
}

// historyFileAllowed проверяет, что удалять можно только свой звук внутри
// папки данных: id приходит из окна, и путь из него не должен вывести наружу.
func (a *App) historyFileAllowed(path string) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)
	root := filepath.Clean(a.Media)
	if !strings.HasPrefix(strings.ToLower(clean), strings.ToLower(root)+string(filepath.Separator)) {
		return false
	}
	switch strings.ToLower(filepath.Ext(clean)) {
	case ".wav", ".mp3", ".m4a", ".ogg", ".opus", ".flac", ".aac":
		return true
	}
	return false
}

// recordingSeconds — длительность записи в любом из форматов, которыми пишет
// диктовка и её прежние версии.
//
// У WAV длина берётся из заголовка (и из самого файла, если заголовок отстал), у
// Ogg/Opus — по позиции последней целой страницы: глобального оглавления у Ogg
// нет, и позиция страницы и есть длина. У старых .m4a длины в файле может не
// быть вовсе — тогда вызывающий получает 0 и решает сам.
func recordingSeconds(path string) (float64, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ogg", ".opus":
		return opusrec.Seconds(path)
	}
	return wavSeconds(path)
}

// wavSeconds считает длительность WAV по его заголовку. Нужна для сверки со
// сжатой копией: если кодировщик соврал или оборвался, сжатый файл короче
// записи, и удалять WAV нельзя.
func wavSeconds(path string) (float64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, 44)
	if _, err := file.Read(header); err != nil {
		return 0, false
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return 0, false
	}
	byteRate := int64(binary.LittleEndian.Uint32(header[28:]))
	dataSize := int64(binary.LittleEndian.Uint32(header[40:]))
	// Размер в заголовке может отставать на секунды, если запись оборвалась;
	// тогда о длительности говорит сам файл.
	if info, err := file.Stat(); err == nil && info.Size() > 44 && 44+dataSize > info.Size() {
		dataSize = info.Size() - 44
	}
	if byteRate <= 0 || dataSize <= 0 {
		return 0, false
	}
	return float64(dataSize) / float64(byteRate), true
}

// sameDuration — сходятся ли длительности с точностью до секунды и двух
// процентов: кодировщик теряет доли секунды на границах кадров, и требовать
// побайтового совпадения значило бы браковать исправную запись.
func sameDuration(left, right float64) bool {
	if left <= 0 || right <= 0 {
		return true
	}
	tolerance := math.Max(1, 0.02*left)
	return math.Abs(left-right) <= tolerance
}
