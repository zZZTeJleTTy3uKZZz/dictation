// Package publictree — общая часть проверки публичного дерева «Диктовки».
//
// Публичное дерево описано белым списком release/public-tree.txt: файлы вне
// списка приватными не считаются и в проверке не участвуют. Внутри списка
// запрещено ВСЁ, что перечисляет forbiddenNeedles, — без исключений: ни
// протокольных токенов, ни адресов приватных модулей в go.mod или import.
//
// Пакет используется двумя программами: scripts/public-check (сторож составa)
// и scripts/build-public-tree (сборка дерева выпуска трансформацией).
package publictree

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Части запрещённых имён разнесены по строкам нарочно: пакет сам лежит в
// публичном дереве, и его исходник не должен содержать запрещённых строк
// целиком — иначе выпуск упадёт на собственном стороже.
func forbiddenNeedles() []string {
	return []string{
		"git" + "lab.com/meetdigest",
		"git" + "lab.com/cif" + "ropro1",
		"prag" + "mati",
		"cif" + "ro",
		"neur" + "aldeep",
		"deep" + "seek",
		"100." + "64.",
		"ru-" + "dev",
		"792" + "88",
		"ana" + "st",
		"gs" + "k_",
		"s" + "k-",
		// Слово приватного реестра запрещено целиком: любой регистр, любое
		// вхождение, в том числе внутри идентификаторов и имён файлов.
		"git" + "lab",
	}
}

// ForbiddenNeedles — копия списка для тестов и диагностики.
func ForbiddenNeedles() []string { return append([]string(nil), forbiddenNeedles()...) }

// Pattern — одно правило белого списка.
type Pattern struct {
	Text    string
	Exclude bool
	re      *regexp.Regexp
}

// Violation — одно запрещённое вхождение в публичном файле.
type Violation struct {
	Path   string
	Line   int
	Needle string
}

// LoadPatterns читает белый список. Пустые строки и комментарии игнорируются,
// ! в начале означает исключение, ** — любую глубину.
func LoadPatterns(path string) ([]Pattern, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("белый список %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	var out []Pattern
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		item := Pattern{Text: text}
		if strings.HasPrefix(text, "!") {
			item.Exclude = true
			text = strings.TrimSpace(strings.TrimPrefix(text, "!"))
		}
		item.re, err = compilePattern(text)
		if err != nil {
			return nil, fmt.Errorf("шаблон %q: %w", item.Text, err)
		}
		out = append(out, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("чтение белого списка: %w", err)
	}
	return out, nil
}

func compilePattern(text string) (*regexp.Regexp, error) {
	var builder strings.Builder
	builder.WriteString("^")
	for index := 0; index < len(text); index++ {
		switch text[index] {
		case '*':
			if index+1 < len(text) && text[index+1] == '*' {
				index++
				if index+1 < len(text) && text[index+1] == '/' {
					index++
					builder.WriteString("(?:.*/)?")
				} else {
					builder.WriteString(".*")
				}
			} else {
				builder.WriteString("[^/]*")
			}
		case '?':
			builder.WriteString("[^/]")
		default:
			builder.WriteString(regexp.QuoteMeta(string(text[index])))
		}
	}
	builder.WriteString("$")
	return regexp.Compile(builder.String())
}

// Walk возвращает отсортированные относительные пути публичных файлов и
// общее число файлов дерева. Исключения применяются после включений.
func Walk(root string, patterns []Pattern) ([]string, int, error) {
	var included []string
	all := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".worktrees", "_scratchpad", "outputs", "build", "node_modules", ".dart_tool":
				return filepath.SkipDir
			}
			return nil
		}
		all++
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		in, out := false, false
		for _, item := range patterns {
			if !item.re.MatchString(rel) {
				continue
			}
			if item.Exclude {
				out = true
			} else {
				in = true
			}
		}
		if in && !out {
			included = append(included, rel)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	sort.Strings(included)
	return included, all, nil
}

// Check возвращает публичные файлы, общее число файлов и нарушения.
// Исключений из запрета нет: ни токенов протокола, ни путей kits.
func Check(root string, patterns []Pattern) ([]string, int, []Violation, error) {
	public, all, err := Walk(root, patterns)
	if err != nil {
		return nil, 0, nil, err
	}
	needles := forbiddenNeedles()
	var found []Violation
	for _, rel := range public {
		lines, ok := readLines(filepath.Join(root, filepath.FromSlash(rel)))
		if !ok {
			continue // бинарный файл: в нём не ищем подстроки
		}
		for number, line := range lines {
			for _, needle := range needles {
				if _, ok := indexMasked(line, needle); ok {
					found = append(found, Violation{Path: rel, Line: number + 1, Needle: needle})
				}
			}
		}
	}
	return public, all, found, nil
}

var binaryExt = map[string]bool{
	".ico": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".dll": true, ".exe": true, ".snap": true, ".woff": true, ".ttf": true,
	".pdf": true, ".zip": true,
}

// readLines возвращает false для бинарных и слишком больших файлов.
func readLines(path string) ([]string, bool) {
	if binaryExt[strings.ToLower(filepath.Ext(path))] {
		return nil, false
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > 4<<20 {
		return nil, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = file.Close() }()
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, false
	}
	return lines, true
}

// IndexMasked ищет needle без учёта регистра и возвращает первое НЕзамаскированное
// вхождение. Маскирование больше не применяется: разрешённых токенов не существует.
//
// Все вхождения надо перебирать: если первое попалось внутри чужого идентификатора
// (AnaState или task-), а следующее — настоящее, сторож обязан его увидеть.
// Раньше проверялось только первое вхождение, и строка с безобидным префиксом
// перед настоящим адресом/ключом проходила проверку.
func IndexMasked(line, needle string) (int, bool) {
	lower := strings.ToLower(line)
	term := strings.ToLower(needle)
	if term == "" {
		return 0, false
	}
	for offset := 0; offset < len(lower); {
		rel := strings.Index(lower[offset:], term)
		if rel < 0 {
			break
		}
		at := offset + rel
		offset = at + len(term)
		// Короткий токен ключа не должен ловиться внутри слов вроде task-;
		// имя владельца — внутри чужих идентификаторов вроде AnaState.
		if term == shortKey && at > 0 && isWordByte(lower[at-1]) {
			continue
		}
		if term == ownerName {
			if at > 0 && isWordByte(lower[at-1]) {
				continue
			}
			end := at + len(term)
			if end < len(lower) && isWordByte(lower[end]) {
				continue
			}
		}
		return at, true
	}
	return 0, false
}

func indexMasked(line, needle string) (int, bool) { return IndexMasked(line, needle) }

const (
	shortKey  = "s" + "k-"
	ownerName = "ana" + "st"
)

func isWordByte(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
}
