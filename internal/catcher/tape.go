// Настоящая запись: микрофон, звук встречи и, если просили, экран.
package catcher

import (
	"log/slog"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/recorder"
)

// openRecorder — собрать запись по настройкам.
//
// Отдельно от ловца, потому что в проверках на её место становится пустышка:
// настоящая запись открывает микрофон, а делать это посреди проверки нельзя.
func openRecorder(settings *prefs.Recording, folder string, log *slog.Logger) Tape {
	made := recorder.New(folder, settings.Bitrate(), log)
	made.Screen = settings.Screen
	made.ScreenWidth = settings.ScreenWidth()
	made.ScreenTarget = settings.ScreenTarget
	return made
}
