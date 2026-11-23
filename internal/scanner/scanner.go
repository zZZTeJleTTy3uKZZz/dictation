// Package scanner — обход папок: что уже видели, что дописывается, что пора брать.
//
// Порт meetdigest_watcher/scanner.py. Логика не трогает ни сеть, ни файлы
// наугад: время и содержимое папки приходят аргументами, поэтому поведение
// проверяется без ожиданий.
package scanner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// stableChecks — файл считается дописанным, когда размер не изменился между
// обходами. Один обход — не доказательство: запись растёт медленно, и часовая
// встреча выглядит «готовой» ещё полчаса.
const stableChecks = 2

// Candidate — запись, готовая к отправке.
type Candidate struct {
	Path string
	Size int64
}

// Journal — что уже отправлено.
//
// Переживает перезапуск, иначе после рестарта уйдёт всё заново: человек
// получит двадцать конспектов по одной встрече, а сервис — счёт за каждый.
type Journal struct {
	path string
	mu   sync.Mutex
	seen map[string]int64
}

func NewJournal(path string) *Journal {
	made := &Journal{path: path, seen: map[string]int64{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return made
	}
	if err := json.Unmarshal(raw, &made.seen); err != nil {
		// Журнал повреждён — начинаем с чистого. Хуже, чем повтор отправки,
		// только молчание: к отправленному вернёмся, к пропущенному нет.
		made.seen = map[string]int64{}
	}
	return made
}

func (j *Journal) IsNew(path string, size int64) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	known, ok := j.seen[path]
	return !ok || known != size
}

func (j *Journal) Remember(path string, size int64) error {
	j.mu.Lock()
	j.seen[path] = size
	raw, err := json.MarshalIndent(j.seen, "", "  ")
	j.mu.Unlock()
	if err != nil {
		return fmt.Errorf("сложить журнал: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
		return fmt.Errorf("завести папку журнала: %w", err)
	}
	if err := os.WriteFile(j.path, raw, 0o644); err != nil {
		return fmt.Errorf("записать журнал: %w", err)
	}
	return nil
}

// Scanner — ищет в папках новые записи и отдаёт только дописанные до конца.
type Scanner struct {
	mu          sync.Mutex
	directories []string
	journal     *Journal
	extensions  []string
	maxAge      time.Duration
	// sizes — что видели в прошлый обход: размер и сколько раз он повторился
	sizes map[string][2]int64
}

func New(directories []string, journal *Journal, extensions []string, maxAgeHours float64) *Scanner {
	lowered := make([]string, 0, len(extensions))
	for _, one := range extensions {
		lowered = append(lowered, strings.ToLower(one))
	}
	return &Scanner{
		directories: directories,
		journal:     journal,
		extensions:  lowered,
		maxAge:      time.Duration(maxAgeHours * float64(time.Hour)),
		sizes:       map[string][2]int64{},
	}
}

// Directories — за чем следим сейчас.
func (s *Scanner) Directories() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.directories...)
}

// Watch — сменить набор папок на ходу.
//
// Человек добавляет папку в настройках и ждёт, что записи оттуда поедут
// сейчас, а не после перезапуска, про который он и не знает. Возвращает true,
// если набор действительно поменялся: об этом стоит написать в журнал, а про
// каждый обход — нет.
func (s *Scanner) Watch(directories []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sameSet(s.directories, directories) {
		return false
	}
	s.directories = append([]string(nil), directories...)
	// Забываем замеры у папок, которых больше нет: иначе словарь растёт весь
	// день, а толку от него ноль.
	known := map[string]bool{}
	for _, path := range s.files() {
		known[path] = true
	}
	for path := range s.sizes {
		if !known[path] {
			delete(s.sizes, path)
		}
	}
	return true
}

// files — записи в папках наблюдения.
func (s *Scanner) files() []string {
	found := make([]string, 0, 16)
	for _, directory := range s.directories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			// Файлы с точки — служебные: так помечает свои недописанные
			// дорожки ловец встреч, и отправлять их посреди разговора нельзя.
			if strings.HasPrefix(name, ".") {
				continue
			}
			if !s.suits(name) {
				continue
			}
			found = append(found, filepath.Join(directory, name))
		}
	}
	sort.Strings(found)
	return found
}

func (s *Scanner) suits(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, one := range s.extensions {
		if ext == one {
			return true
		}
	}
	return false
}

// Scan — один обход. Файл отдаётся, когда его размер повторился между обходами.
func (s *Scanner) Scan(now time.Time) []Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()

	ready := make([]Candidate, 0, 4)
	for _, path := range s.files() {
		info, err := os.Stat(path)
		if err != nil {
			continue // файл забрали прямо во время обхода
		}
		size := info.Size()
		if size == 0 || !s.journal.IsNew(path, size) {
			continue
		}

		// Архив трогать не надо: в папке лежат записи прошлых месяцев.
		if s.maxAge > 0 && now.Sub(info.ModTime()) > s.maxAge {
			_ = s.journal.Remember(path, size)
			continue
		}

		previous, repeats := int64(-1), int64(0)
		if seen, ok := s.sizes[path]; ok {
			previous, repeats = seen[0], seen[1]
		}
		if size == previous {
			repeats++
		} else {
			repeats = 1
		}
		s.sizes[path] = [2]int64{size, repeats}

		// Растущий файл — это идущая запись, забирать её рано.
		if repeats >= stableChecks {
			ready = append(ready, Candidate{Path: path, Size: size})
		}
	}
	return ready
}

// Accept — отметить запись отправленной.
func (s *Scanner) Accept(candidate Candidate) error {
	s.mu.Lock()
	delete(s.sizes, candidate.Path)
	s.mu.Unlock()
	return s.journal.Remember(candidate.Path, candidate.Size)
}

func sameSet(one, other []string) bool {
	if len(one) != len(other) {
		return false
	}
	for index := range one {
		if one[index] != other[index] {
			return false
		}
	}
	return true
}

// TitleFromFilename — как назвать встречу по имени файла.
//
// Телемост пишет «Встреча в Телемосте 07.09.26 13-59-55 — запись.webm»:
// убираем расширение и служебный хвост, остальное человек узнаёт.
func TitleFromFilename(path string) string {
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	for _, tail := range []string{" — запись", " - запись", " (два канала)"} {
		name = strings.TrimSuffix(name, tail)
	}
	return strings.TrimSpace(name)
}

// FindChatFile — текст чата встречи, если Телемост положил его рядом.
//
// Он кладёт «…— чат.txt» рядом с записью: в чате бывают ссылки и имена, и
// конспект с ними получается точнее.
func FindChatFile(record string) string {
	base := strings.TrimSuffix(filepath.Base(record), filepath.Ext(record))
	base = strings.TrimSuffix(base, " — запись")
	base = strings.TrimSuffix(base, " - запись")
	directory := filepath.Dir(record)
	for _, candidate := range []string{
		base + " — чат.txt", base + " - чат.txt", base + ".txt",
	} {
		path := filepath.Join(directory, candidate)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}
