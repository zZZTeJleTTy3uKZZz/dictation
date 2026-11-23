package settingsapi

import (
	"fmt"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/config"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/notes"
)

// NoteText — заменить текст заметки, не трогая всё остальное.
//
// Заметка — это сперва звук и только потом текст: расшифровка может не
// получиться, и тогда остаётся запись, которую человек послушает сам. Поэтому
// здесь меняется ровно текст, а время, устройство, источник и путь к звуку
// остаются как были: их пишет диктофон, и правка текста не повод их терять.
//
// Пустой текст допускается нарочно: человек вправе стереть то, что
// распознавание написало неверно, и оставить одну запись.
func NoteText(id, text string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("нужен номер заметки")
	}
	store := notes.NewStore(notes.DefaultPath(config.Home()))
	return store.Update(id, func(note *notes.Note) {
		note.Text = text
	})
}
