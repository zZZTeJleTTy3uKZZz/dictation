package localdict

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/appdirs"
)

// recordingLimit bounds how many session files are kept. The newest ones stay;
// the audio itself is what makes a failed recognition recoverable.
const recordingLimit = 200

// historyLimit bounds the history. The file is rewritten after every session, so
// it must not grow forever. Один предел на всех: прежде одно место резало до
// двухсот, другое до ста, и история выходила разной длины в зависимости от
// того, кто её записал.
const historyLimit = 100

func trimHistory(history []Entry) []Entry {
	if len(history) <= historyLimit {
		return history
	}
	return history[:historyLimit]
}

// Settings live outside the user's profile. A cleanup tool that empties
// %LOCALAPPDATA% took the hotkey, the providers with their protected keys and
// the whole history with it; preferences are small and must survive that.
// The session audio stays in the profile, where large per-user files belong, and
// the endpoint stays there too: it is per-user state, not a preference.
func SettingsDir() string {
	return appdirs.SettingsDir()
}

// DataDir holds the session recordings, the decode fragments and the loopback
// endpoint file.
func DataDir() string {
	return appdirs.DataDir()
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

// migrateSettings moves preferences written by earlier versions out of the
// profile once. The old copies stay where they are: a half-finished move must
// not be able to lose the only copy.
func migrateSettings(settings, data string) {
	if samePath(settings, data) {
		return
	}
	for _, name := range stateFiles {
		if _, err := os.Stat(filepath.Join(settings, name)); err == nil {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(data, name))
		if err != nil {
			continue
		}
		if os.MkdirAll(settings, 0700) != nil {
			return
		}
		_ = os.WriteFile(filepath.Join(settings, name), payload, 0600)
	}
}

// pruneRecordings оставляет limit самых новых записей в папке и убирает
// остальные. Сортировка по имени: имена начинаются с даты-времени.
//
// Предел ОДИН на все расширения. Прежде чистка звалась трижды — для *.wav,
// *.m4a и *.mp3 по 200 штук, — и на рабочей машине накопилось 616 файлов на
// 252 МБ: каждый формат держал свой предел, а вместе они давали втрое больше.
func pruneRecordings(dir string, limit int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	if len(names) <= limit {
		return
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-limit] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// pruneFiles keeps the newest limit files by name (session names start with the
// date) and removes the rest. Large folders must not grow without a bound.
func pruneFiles(pattern string, limit int) {
	paths, err := filepath.Glob(pattern)
	if err != nil || len(paths) <= limit {
		return
	}
	for _, path := range paths[:len(paths)-limit] {
		_ = os.Remove(path)
	}
}
