// Package sources — откуда брать записи: провайдеры и папки, выбранные человеком.
//
// Порт meetdigest_watcher/sources.py, включая формат файла: настройки лежат в
// ~/.meetdigest/sources.json, и обе версии воркера читают один и тот же файл.
// Так переключение с Python на Go не теряет выбор человека.
//
// ПОЧЕМУ ПРОВАЙДЕР, А НЕ ПРОСТО ПУТЬ. «Телемост» — это не папка, а правило:
// записи лежат в «Документах», но «Документы» бывают перенаправлены в OneDrive,
// и у одной программы путь на двух машинах разный. Поэтому включается источник,
// а куда именно смотреть — его забота.
package sources

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/appdirs"
)

// DefaultPath — где лежат настройки источников.
func DefaultPath() string {
	home := appdirs.HomeDir()
	if home == "" {
		return "sources.json"
	}
	return filepath.Join(home, ".meetdigest", "sources.json")
}

// Sources — что сейчас под наблюдением.
type Sources struct {
	Path string `json:"-"`
	// Telemost — Телемост как источник: включён, папка ищется сама.
	Telemost bool `json:"telemost"`
	// Folders — свои папки, добавленные в настройках.
	Folders []string `json:"folders"`
}

// Load — прочитать настройки источников.
//
// Испорченный файл — не повод перестать следить за Телемостом: молчащий
// воркер человек заметит на день позже, чем пропажу папки.
func Load(path string) *Sources {
	if path == "" {
		path = DefaultPath()
	}
	made := &Sources{Path: path, Telemost: true}
	raw, err := os.ReadFile(path)
	if err != nil {
		return made
	}
	var stored struct {
		Telemost *bool    `json:"telemost"`
		Folders  []string `json:"folders"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return made
	}
	if stored.Telemost != nil {
		made.Telemost = *stored.Telemost
	}
	for _, folder := range stored.Folders {
		if strings.TrimSpace(folder) != "" {
			made.Folders = append(made.Folders, folder)
		}
	}
	return made
}

func (s *Sources) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(struct {
		Telemost bool     `json:"telemost"`
		Folders  []string `json:"folders"`
	}{s.Telemost, s.Folders}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, append(raw, '\n'), 0o644)
}

// Add — добавить свою папку. Уже добавленная — не повод для второй строки.
func (s *Sources) Add(folder string) bool {
	chosen := clean(folder)
	if chosen == "" {
		return false
	}
	for _, known := range s.Folders {
		if strings.EqualFold(clean(known), chosen) {
			return false
		}
	}
	s.Folders = append(s.Folders, chosen)
	return true
}

func (s *Sources) Remove(folder string) {
	target := strings.ToLower(clean(folder))
	kept := make([]string, 0, len(s.Folders))
	for _, known := range s.Folders {
		if strings.ToLower(clean(known)) != target {
			kept = append(kept, known)
		}
	}
	s.Folders = kept
}

func (s *Sources) ToggleTelemost() { s.Telemost = !s.Telemost }

// Directories — все папки под наблюдением: провайдеры плюс свои, без повторов.
func (s *Sources) Directories() []string {
	found := make([]string, 0, len(s.Folders)+2)
	if s.Telemost {
		found = append(found, TelemostDirectories()...)
	}
	for _, folder := range s.Folders {
		found = append(found, clean(folder))
	}
	return unique(found)
}

// TelemostPlaces — где Телемост держит записи, по убыванию вероятности.
//
// Список, а не один путь: на Windows «Документы» сплошь и рядом перенаправлены
// в OneDrive, и обычного пути на такой машине просто нет.
func TelemostPlaces() []string {
	home := appdirs.HomeDir()
	if home == "" {
		return nil
	}
	places := []string{
		filepath.Join(home, "Documents", "Телемост"),
		filepath.Join(home, "OneDrive", "Documents", "Телемост"),
		filepath.Join(home, "OneDrive", "Документы", "Телемост"),
		filepath.Join(home, "Документы", "Телемост"),
	}
	// OneDrive с названием организации: «OneDrive - Компания».
	if matches, err := filepath.Glob(filepath.Join(home, "OneDrive*")); err == nil {
		sort.Strings(matches)
		for _, folder := range matches {
			info, err := os.Stat(folder)
			if err != nil || !info.IsDir() {
				continue
			}
			for _, sub := range []string{"Documents", "Документы"} {
				places = append(places, filepath.Join(folder, sub, "Телемост"))
			}
		}
	}
	return unique(places)
}

// TelemostDirectories — папки Телемоста, которые есть на диске.
//
// Ни одной нет — ждём появления там, где Телемост её и создаст: папка
// заводится в момент первой записи, и до тех пор следить не за чем.
func TelemostDirectories() []string {
	places := TelemostPlaces()
	found := make([]string, 0, len(places))
	for _, place := range places {
		if info, err := os.Stat(place); err == nil && info.IsDir() {
			found = append(found, place)
		}
	}
	if len(found) == 0 && len(places) > 0 {
		return places[:1]
	}
	return found
}

func clean(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "~") {
		if home := appdirs.HomeDir(); home != "" {
			trimmed = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(trimmed, "~"), "/"))
		}
	}
	return filepath.Clean(trimmed)
}

func unique(values []string) []string {
	seen := make(map[string]bool, len(values))
	found := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		found = append(found, value)
	}
	return found
}
