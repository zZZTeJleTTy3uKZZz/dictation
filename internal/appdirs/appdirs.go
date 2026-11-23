// Package appdirs — одна точка, откуда код берёт каталоги настроек и данных,
// и сторож изоляции проверок.
//
// В боевой программе Guard ничего не делает: корень подмены задаёт только
// TestMain пакета через EnableTestGuard. Благодаря этому проверка, которая
// собралась прочитать или записать настоящий каталог человека, падает сразу,
// а не тихо портит хранилище ключей.
package appdirs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// testRoot — временный каталог текущего тестового процесса. Пуст в бою.
var testRoot string

// EnableTestGuard включает сторож путей. Зовётся только из TestMain; в боевой
// программе переменная остаётся пустой и все проверки ниже стоит на быстром пути.
func EnableTestGuard(root string) { testRoot = root }

// Guard падает, если проверка собралась работать вне своего временного каталога.
// Относительные пути не разрешены: они ушли бы в рабочий каталог пакета.
func Guard(path string) {
	if testRoot == "" {
		return
	}
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(filepath.Clean(testRoot), clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		panic(fmt.Sprintf("test isolation: путь %q вне временного каталога %q", path, testRoot))
	}
}

// settingsDir — готовый каталог настроек. Переопределение из окружения — это
// УЖЕ каталог настроек, а не основание: добавлять к нему «Dictation» нельзя,
// иначе проверки и боевые запуски с подменой смотрят в разные места.
func settingsDir(override, programData, localAppData, tmp string) string {
	if dir := strings.TrimSpace(override); dir != "" {
		return dir
	}
	base := programData
	if base == "" {
		base = localAppData
	}
	if base == "" {
		base = tmp
	}
	return filepath.Join(base, "Dictation")
}

// dataDir — готовый каталог данных (записи, endpoint, обломки). Правило то же:
// DICTATION_DATA_DIR задаёт каталог целиком.
func dataDir(override, localAppData, tmp string) string {
	if dir := strings.TrimSpace(override); dir != "" {
		return dir
	}
	base := localAppData
	if base == "" {
		base = tmp
	}
	return filepath.Join(base, "Dictation")
}

// localRoot — личная папка LOCALAPPDATA; на машинах без неё — из профиля.
func localRoot(localAppData, userProfile string) string {
	if localAppData != "" {
		return localAppData
	}
	return filepath.Join(userProfile, "AppData", "Local")
}

// SettingsDir — папка настроек службы: DICTATION_SETTINGS_DIR, иначе
// ProgramData/Dictation, LOCALAPPDATA/Dictation или временный каталог.
func SettingsDir() string {
	dir := settingsDir(
		os.Getenv("DICTATION_SETTINGS_DIR"),
		os.Getenv("ProgramData"),
		os.Getenv("LOCALAPPDATA"),
		os.TempDir(),
	)
	Guard(dir)
	return dir
}

// DataDir — папка данных службы: DICTATION_DATA_DIR, иначе LOCALAPPDATA/Dictation
// или временный каталог.
func DataDir() string {
	dir := dataDir(os.Getenv("DICTATION_DATA_DIR"), os.Getenv("LOCALAPPDATA"), os.TempDir())
	Guard(dir)
	return dir
}

// LocalRoot — личная папка LOCALAPPDATA, откуда ищут endpoint и службу.
func LocalRoot() string {
	root := localRoot(os.Getenv("LOCALAPPDATA"), os.Getenv("USERPROFILE"))
	Guard(root)
	return root
}

// TempRoot — временный каталог процесса. В проверках он лежит внутри корня
// подмены, поэтому t.TempDir и брошенные файлы не уходят в общий /tmp.
func TempRoot() string {
	root := os.TempDir()
	Guard(root)
	return root
}

// HomeDir — домашний каталог человека. Пусто, если ОС его не знает: мелкие
// старые ветки сами решают, что делать с отсутствующим домом.
func HomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	Guard(home)
	return home
}
