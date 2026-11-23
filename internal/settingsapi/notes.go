// История заметок для внешних интерфейсов.
//
// Тот же принцип, что у настроек: окно спрашивает готовый список и ничего не
// вычисляет само — ни как подписать заметку, ни как показать длительность.
// Иначе два интерфейса подписали бы одну заметку по-разному.
package settingsapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/config"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hidden"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/notes"
)

// NoteView — заметка, как её показывает окно.
type NoteView struct {
	ID string `json:"id"`
	// At — в том виде, в котором это читает человек: окну не придётся знать, в
	// каком часовом поясе живёт машина и как в его языке пишут дату.
	At   string `json:"at"`
	When string `json:"when"`
	// Length — «0:07», а не 7.2 секунды.
	Length string `json:"length"`
	Device string `json:"device"`
	Short  string `json:"short"`
	Text   string `json:"text"`
	// Speech — чем расшифровано; видно, чей это текст.
	Speech  string `json:"speech"`
	Trouble string `json:"trouble"`
	// Source — откуда заметка пришла: из значка или из Harness. Окно показывает
	// это словами и по нему же фильтрует.
	Source string `json:"source"`
	// HasAudio — есть ли что послушать: файл мог быть удалён вручную или по
	// сроку хранения, а кнопка «послушать» не должна вести в пустоту.
	HasAudio bool `json:"has_audio"`
}

// Notes — история заметок, свежие впереди.
func Notes(search string, limit int) ([]NoteView, error) {
	store := notes.NewStore(notes.DefaultPath(config.Home()))
	found, err := store.Search(search, limit)
	if err != nil {
		return nil, err
	}

	shown := make([]NoteView, 0, len(found))
	for _, note := range found {
		shown = append(shown, showNote(note))
	}
	return shown, nil
}

// Note — одна заметка целиком (в списке текст подрезан).
func Note(id string) (NoteView, error) {
	store := notes.NewStore(notes.DefaultPath(config.Home()))
	note, err := store.Get(id)
	if err != nil {
		return NoteView{}, err
	}
	return showNote(note), nil
}

// ForgetNote — убрать заметку вместе со звуком.
func ForgetNote(id string) error {
	store := notes.NewStore(notes.DefaultPath(config.Home()))
	return store.Forget(id)
}

// PlayNote — открыть звук заметки тем, чем человек слушает музыку.
//
// Своего проигрывателя не делаем: в системе он уже есть, и человек знает, как им
// пользоваться.
func PlayNote(id string) error {
	store := notes.NewStore(notes.DefaultPath(config.Home()))
	note, err := store.Get(id)
	if err != nil {
		return err
	}
	if note.Audio == "" {
		return fmt.Errorf("у заметки нет звука")
	}
	if _, err := os.Stat(note.Audio); err != nil {
		return fmt.Errorf("файл заметки не найден: %s", filepath.Base(note.Audio))
	}
	return hidden.Command("rundll32", "url.dll,FileProtocolHandler", note.Audio).Start()
}

func showNote(note notes.Note) NoteView {
	audio := note.Audio != ""
	if audio {
		if _, err := os.Stat(note.Audio); err != nil {
			audio = false
		}
	}
	return NoteView{
		ID:       note.ID,
		At:       note.At.Format("02.01.2006 15:04"),
		When:     ago(note.At),
		Length:   length(note.Seconds),
		Device:   note.Device,
		Short:    note.Short(),
		Text:     strings.TrimSpace(note.Text),
		Speech:   speechTitle(note.Speech),
		Trouble:  note.Trouble,
		Source:   sourceTitle(note.Source),
		HasAudio: audio,
	}
}

// sourceTitle — человеческое имя того, откуда пришла заметка.
//
// Пустое остаётся пустым: у заметок, записанных до появления признака, источника
// нет, и придумывать его задним числом значит врать в истории.
func sourceTitle(name string) string {
	switch name {
	case notes.SourceWidget:
		return "значок"
	case notes.SourceHarness:
		return "Harness"
	default:
		return name
	}
}

// length — длительность словами человека.
func length(seconds float64) string {
	whole := int(seconds + 0.5)
	return fmt.Sprintf("%d:%02d", whole/60, whole%60)
}

// ago — «минуту назад», «вчера».
//
// Для свежих заметок это понятнее даты: человек помнит «только что наговорил», а
// не «15:42».
func ago(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	passed := time.Since(at)
	switch {
	case passed < time.Minute:
		return "только что"
	case passed < time.Hour:
		return fmt.Sprintf("%d мин назад", int(passed.Minutes()))
	case passed < 24*time.Hour:
		return fmt.Sprintf("%d ч назад", int(passed.Hours()))
	case passed < 48*time.Hour:
		return "вчера"
	case passed < 7*24*time.Hour:
		return fmt.Sprintf("%d дн назад", int(passed.Hours()/24))
	default:
		return at.Format("02.01.2006")
	}
}

// speechTitle — человеческое имя способа расшифровки.
//
// Значение приходит из общего словаря каналов (internal/speech), и его пишут обе
// половины диктовки: значок и плагин Harness. Прежнее слово значка («диктовка»)
// осталось в записях, сделанных до уговора, — читаем и его: иначе старые заметки
// потеряли бы способ, а выборка по способу теряла бы их вовсе.
func speechTitle(name string) string {
	switch name {
	case "dictation", "диктовка":
		return "служба диктовки"
	case "local":
		return "локальная модель"
	case "gigaam":
		return "локальная модель GigaAM"
	case "transcribe":
		return "наш transcribe"
	case "api":
		return "сервис по сети"
	case "":
		return ""
	default:
		return name
	}
}
