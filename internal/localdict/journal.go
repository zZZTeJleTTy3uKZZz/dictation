package localdict

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const journalLimit = 200

type journalEvent struct {
	Event       string    `json:"event"`
	At          time.Time `json:"at"`
	Text        string    `json:"text,omitempty"`
	Destination string    `json:"destination,omitempty"`
	Target      bool      `json:"target,omitempty"`
	// Audio — файл записи этого разговора. Пишется в start, чтобы после
	// вылета было что расшифровать и что показать человеку.
	Audio string `json:"audio,omitempty"`
	// Status — чем кончился разговор: done, error, silence или cancelled.
	// По нему восстановление отличает неудачную расшифровку от оборванной.
	Status string `json:"status,omitempty"`
}

// A journal writes recognized words to disk while they are still being spoken.
// A lost process, a flat battery or a killed supervisor must not cost the user
// the conversation: what was already recognized is on disk, and the next start
// offers it as a history entry he can copy.
type journal struct {
	file  *os.File
	path  string
	began time.Time
	last  time.Time
}

func openJournal(dir, audio string) (*journal, error) {
	folder := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(folder, 0700); err != nil {
		return nil, err
	}
	now := time.Now()
	path := filepath.Join(folder, now.Format("20060102-150405")+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	made := &journal{file: file, path: path, began: now, last: now}
	if err = made.write(journalEvent{Event: "start", At: now, Audio: audio}); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	pruneFiles(filepath.Join(folder, "*.jsonl"), journalLimit)
	return made, nil
}

func (j *journal) write(event journalEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err = j.file.Write(append(payload, '\n')); err != nil {
		return err
	}
	return j.file.Sync()
}

// append stores one committed fragment. Nothing is rewritten: the file is an
// append-only record, so a partial line is the worst a crash can leave behind.
func (j *journal) append(text string) {
	if j == nil || text == "" {
		return
	}
	now := time.Now()
	if err := j.write(journalEvent{Event: "words", At: now, Text: text}); err == nil {
		j.last = now
	}
}

// finish marks the recording complete so recovery does not offer it twice.
//
// В событии едет и статус: «finish» бывает у удачного разговора, у тишины и у
// отказа распознавания, и восстановлению нужно отличать одно от другого, а не
// догадываться по пустому тексту.
func (j *journal) finish(destination, text, status string) {
	if j == nil {
		return
	}
	event := "finish"
	if status == "error" {
		// Неудачный разговор — отдельное событие, а не finish со странным
		// статусом: по имени события видно, чем кончилась сессия, не разбирая
		// поля.
		event = "error"
	}
	_ = j.write(journalEvent{Event: event, At: time.Now(), Text: text, Destination: destination, Status: status, Audio: j.audio()})
	_ = j.file.Close()
}

// audio называет файл записи, который лёг в start.
func (j *journal) audio() string {
	if j == nil {
		return ""
	}
	payload, err := os.ReadFile(j.path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(payload), "\n") {
		var event journalEvent
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event.Event == "start" {
			return event.Audio
		}
	}
	return ""
}

func (j *journal) close() {
	if j != nil && j.file != nil {
		_ = j.file.Close()
	}
}

// journalSeconds — длительность разговора из журнала.
func journalSeconds(session journalSession) float64 {
	seconds := session.Ended.Sub(session.Began).Seconds()
	if seconds < 0 {
		return 0
	}
	return seconds
}

// sessionID — устойчивое имя сессии по файлу журнала: 20261111-193914.
func sessionID(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// markRecovered помечает журнал разобранным и дописывает в него, чем кончилось
// восстановление. Так одна и та же сессия не предлагается дважды.
//
// Зовётся ТОЛЬКО после того, как история уже записана на диск: обратный порядок
// и потерял запись 20261111-231748 — журнал был помечен recovered, а строки в
// history.json не оказалось.
func markRecovered(path string, entry Entry) bool {
	return appendJournal(path, journalEvent{
		Event:       "recovered",
		At:          time.Now(),
		Text:        entry.Text,
		Destination: entry.Destination,
		Audio:       entry.Audio,
		Status:      entry.Status,
	})
}

// markDeleted оставляет в журнале метку человека, удалившего запись. Звук
// удаление убирает, но текст журнала пережил бы звук, и без метки
// восстановление вернуло бы удалённую запись при следующем запуске.
func markDeleted(path, audio string) bool {
	return appendJournal(path, journalEvent{Event: "deleted", At: time.Now(), Audio: audio})
}

// markCleared оставляет метку на журнале, когда человек очищает историю
// целиком: очистка — решение, и возвращать эти сессии нельзя.
func markCleared(path string) bool {
	return appendJournal(path, journalEvent{Event: "cleared", At: time.Now()})
}

// markTrimmed оставляет метку на журнале, строку которого вытеснил предел
// истории. Метка на диске переживает и потерю history.json: вытесненное не
// должно вернуться даже тогда, когда история стала короче.
func markTrimmed(path string) bool {
	return appendJournal(path, journalEvent{Event: "trimmed", At: time.Now()})
}

// appendJournal дописывает событие в журнал и сбрасывает его на диск: метка —
// это обещание, что запись больше не появится, и обещание должно пережить сбой
// питания, а не только закрытие файла.
func appendJournal(path string, event journalEvent) bool {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return false
	}
	payload, err := json.Marshal(event)
	if err != nil {
		_ = file.Close()
		return false
	}
	if _, err = file.Write(append(payload, '\n')); err != nil {
		_ = file.Close()
		return false
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return false
	}
	_ = file.Close()
	return true
}

// journalSession — что удалось прочитать из журнала одного разговора.
type journalSession struct {
	Text     string
	Audio    string
	Status   string
	Began    time.Time
	Ended    time.Time
	Finished bool
	// Deleted — человек удалил запись: восстановление её не возвращает.
	Deleted bool
	// Cleared — человек очистил историю целиком: эта сессия в неё не вернётся.
	Cleared bool
	// Trimmed — строку вытеснил предел истории: возвращать её незачем.
	Trimmed bool
}

func readJournal(path string) journalSession {
	var session journalSession
	file, err := os.Open(path)
	if err != nil {
		session.Finished = true
		return session
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	builder := strings.Builder{}
	// Текст итогового события: у журналов прежних версий слова могли лежать
	// только в нём (recovered писали с полным текстом), а не строками words.
	summary := ""
	for scanner.Scan() {
		var event journalEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			// A line cut in half by a crash is the end of what reached the disk.
			break
		}
		switch event.Event {
		case "start":
			session.Began, session.Audio = event.At, event.Audio
		case "words":
			builder.WriteString(event.Text)
			session.Ended = event.At
			if event.Status != "" {
				session.Status = event.Status
			}
		case "finish", "recovered", "error":
			// Итог события не конец чтения: после него в журнал дописывают
			// метку recovered, а затем и deleted, и обе надо увидеть.
			session.Finished = true
			if event.Status != "" {
				session.Status = event.Status
			}
			if strings.TrimSpace(event.Text) != "" {
				summary = event.Text
			}
		case "deleted":
			session.Deleted = true
		case "cleared":
			session.Cleared = true
		case "trimmed":
			session.Trimmed = true
		}
	}
	if builder.Len() > 0 {
		session.Text = builder.String()
	} else {
		session.Text = summary
	}
	return session
}
