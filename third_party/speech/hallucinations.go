package speech

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// builtinFile — встроенный словарь. Лежит рядом с пакетом, потому что embed не
// умеет выходить за пределы каталога пакета.
//
//go:embed data/hallucinations.txt
var builtinFile string

// ErrEmptyDictionary — словарь прочитан, но в нём нет ни одной фразы.
var ErrEmptyDictionary = errors.New("словарь галлюцинаций пуст: нужна хотя бы одна фраза, не считая комментариев")

// Thresholds — пороги, по которым отрезок считается галлюцинацией.
//
// Сильные пороги отсекают отрезок целиком, слабые — только условные фразы из
// словаря («?фраза»): «спасибо за внимание» люди говорят по-настоящему, и
// выбрасывать эту реплику без признака слабой уверенности нельзя.
type Thresholds struct {
	// NoSpeechProb — с какого no_speech_prob отрезок считается шумным.
	NoSpeechProb float64
	// AvgLogprob — ниже какого avg_logprob отрезок ненадёжен.
	AvgLogprob float64
	// CompressionRatio — выше какого отношения сжатия отрезок считается повтором.
	CompressionRatio float64
	// WeakNoSpeechProb и WeakAvgLogprob — те же пороги для условных фраз.
	WeakNoSpeechProb float64
	WeakAvgLogprob   float64
}

// DefaultThresholds — пороги, перенесённые из transcribe/cleanup.py и
// transcribe_router/router9.py без изменений.
func DefaultThresholds() Thresholds {
	return Thresholds{
		NoSpeechProb:     0.6,
		AvgLogprob:       -1.0,
		CompressionRatio: 2.4,
		WeakNoSpeechProb: 0.3,
		WeakAvgLogprob:   -0.8,
	}
}

// Dictionary — разобранный словарь галлюцинаций.
type Dictionary struct {
	exact        []string
	prefixes     []string
	weakExact    []string
	weakPrefixes []string
}

// Empty сообщает, что в словаре нет фраз.
func (d *Dictionary) Empty() bool {
	return d == nil || len(d.exact)+len(d.prefixes)+len(d.weakExact)+len(d.weakPrefixes) == 0
}

// builtin — встроенный словарь. Ошибка разбора встроенного файла — дефект
// сборки, поэтому паника; тест пакета проверяет, что её не происходит.
var builtin = mustParse(builtinFile, "встроенный словарь")

// Builtin возвращает встроенный словарь.
func Builtin() *Dictionary { return builtin }

// Load возвращает встроенный словарь, дополненный содержимым файла path.
//
// Пустой path — только встроенный. Свой файл не заменяет встроенный, а
// дополняет: человек дописывает наблюдения, не переписывая общий список. Файла
// нет или он не в UTF-8 — ошибка: молча потерять дополнения хуже, чем сказать.
func Load(path string) (*Dictionary, error) {
	if strings.TrimSpace(path) == "" {
		return builtin, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("словарь галлюцинаций не прочитан: %s: %w", path, err)
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("словарь галлюцинаций не в UTF-8: %s", path)
	}
	merged := builtinFile + "\n" + string(raw)
	return parse(merged, path)
}

func mustParse(text, source string) *Dictionary {
	d, err := parse(text, source)
	if err != nil {
		panic(err)
	}
	return d
}

// punctuation — знаки, которые заменяются пробелами: сравнение идёт по словам.
var punctuation = regexp.MustCompile(`[^\p{L}\p{N}_\s]`)

func normalize(line string) string {
	return strings.TrimSpace(punctuation.ReplaceAllString(strings.ToLower(line), " "))
}

// parse разбирает словарь из строки. source — откуда текст, для понятной ошибки.
//
// Формат:
//
//	# комментарий
//	фраза            — безусловная подпись субтитров;
//	фраза…           — сравнивается как НАЧАЛО строки;
//	?фраза           — условная: удаляется только при слабой уверенности.
func parse(text, source string) (*Dictionary, error) {
	var d Dictionary
	// Дедупликация отдельно по каждой категории: одна и та же фраза в разных
	// категориях (безусловная и условная) — не повтор.
	seen := map[string]map[string]bool{}
	add := func(dst *[]string, category, key string) {
		if seen[category] == nil {
			seen[category] = map[string]bool{}
		}
		if seen[category][key] {
			return
		}
		seen[category][key] = true
		*dst = append(*dst, key)
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		weak := strings.HasPrefix(line, "?")
		if weak {
			line = strings.TrimSpace(line[1:])
			if line == "" {
				continue
			}
		}
		prefix := false
		switch {
		case strings.HasSuffix(line, "..."):
			line, prefix = line[:len(line)-3], true
		case strings.HasSuffix(line, "…"):
			line, prefix = strings.TrimSuffix(line, "…"), true
		}
		norm := normalize(line)
		if norm == "" {
			continue
		}
		switch {
		case weak && prefix:
			add(&d.weakPrefixes, "wp", norm)
		case weak:
			add(&d.weakExact, "we", norm)
		case prefix:
			add(&d.prefixes, "p", norm)
		default:
			add(&d.exact, "e", norm)
		}
	}
	if d.Empty() {
		return nil, fmt.Errorf("%w (%s)", ErrEmptyDictionary, source)
	}
	return &d, nil
}

// Filter — фильтр галлюцинаций: словарь и пороги.
//
// Нулевые пороги означают умолчания (DefaultThresholds): незаданное поле не
// должно молча выбрасывать всю речь. nil-словарь означает встроенный.
type Filter struct {
	Dictionary *Dictionary
	Thresholds Thresholds
}

// NewFilter собирает фильтр со встроенным словарём и порогами по умолчанию.
func NewFilter() *Filter {
	return &Filter{Dictionary: Builtin(), Thresholds: DefaultThresholds()}
}

// thresholds — пороги фильтра, где незаданное заменено умолчанием.
func (f *Filter) thresholds() Thresholds {
	found := Thresholds{}
	if f != nil {
		found = f.Thresholds
	}
	def := DefaultThresholds()
	if found.NoSpeechProb == 0 {
		found.NoSpeechProb = def.NoSpeechProb
	}
	if found.AvgLogprob == 0 {
		found.AvgLogprob = def.AvgLogprob
	}
	if found.CompressionRatio == 0 {
		found.CompressionRatio = def.CompressionRatio
	}
	if found.WeakNoSpeechProb == 0 {
		found.WeakNoSpeechProb = def.WeakNoSpeechProb
	}
	if found.WeakAvgLogprob == 0 {
		found.WeakAvgLogprob = def.WeakAvgLogprob
	}
	return found
}

// dictionary — словарь фильтра; nil — встроенный.
func (f *Filter) dictionary() *Dictionary {
	if f != nil && f.Dictionary != nil {
		return f.Dictionary
	}
	return Builtin()
}

// Keep сообщает, нужно ли оставить отрезок распознавания.
func (f *Filter) Keep(text string, signals Signals) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	dictionary := f.dictionary()
	thresholds := f.thresholds()
	if isHallucination(dictionary, text, signals, thresholds) {
		return false
	}
	return !lowConfidence(signals, thresholds)
}

// Clean построчно убирает титровые галлюцинации из сплошного текста.
//
// Условные фразы здесь не удаляются: у сплошного текста нет уверенности
// отрезка. Это тот же разбор, что у Keep с пустыми признаками.
func (f *Filter) Clean(text string) string {
	dictionary := f.dictionary()
	thresholds := f.thresholds()
	kept := make([]string, 0)
	for _, line := range strings.Split(text, "\n") {
		if isHallucination(dictionary, line, Signals{}, thresholds) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(blankRuns.ReplaceAllString(strings.Join(kept, "\n"), "\n\n"))
}

// weak — сегмент пришёл со слабой уверенностью.
func weak(signals Signals, thresholds Thresholds) bool {
	return (signals.NoSpeechProb != nil && *signals.NoSpeechProb >= thresholds.WeakNoSpeechProb) ||
		(signals.AvgLogprob != nil && *signals.AvgLogprob < thresholds.WeakAvgLogprob)
}

// Титровые маркеры и глаголы подписи: короткая строка с маркером и инициалами
// или глаголом — подпись автора субтитров, а не речь.
var creditMarkers = []string{"субтитр", "редактор", "корректор", "озвуч", "перевод", "монтаж"}

var creditVerbs = []string{"сделал", "сделала", "читал", "читала", "создавал", "подогнал", "озвучил", "перевёл", "перевел"}

// Инициалы с фамилией в ИСХОДНОЙ строке: «А. Семкин», «И Петров». Проверяется
// регистр: правило на строчных буквах ловит любое однобуквенное слово перед
// словом («к пятнице»), то есть съедает обычную речь.
var initialsName = regexp.MustCompile(`(?:^|[^\p{L}\p{N}])(?:\p{Lu}\.\s*\p{Lu}\p{L}+|\p{Lu}\s+\p{Lu}\p{L}+)`)

// isHallucination сообщает, похожа ли строка на титровую галлюцинацию.
func isHallucination(d *Dictionary, line string, signals Signals, thresholds Thresholds) bool {
	flat := normalize(line)
	if flat == "" || d == nil {
		return false
	}
	if contains(d.exact, flat) {
		return true
	}
	if hasPrefix(d.prefixes, flat) {
		return true
	}
	if weak(signals, thresholds) {
		if contains(d.weakExact, flat) || hasPrefix(d.weakPrefixes, flat) {
			return true
		}
	}
	// Подпись с любым хвостом: короткая строка с маркером и либо инициалами,
	// либо глаголом.
	if len(strings.Fields(flat)) > 6 {
		return false
	}
	if !anyContains(flat, creditMarkers) {
		return false
	}
	if initialsName.MatchString(line) {
		return true
	}
	return anyContains(flat, creditVerbs)
}

// lowConfidence — числовой фильтр отрезка: признаки галлюцинации Whisper.
// Сжатие выше порога достаточно само по себе; no_speech_prob и avg_logprob —
// только вместе.
func lowConfidence(signals Signals, thresholds Thresholds) bool {
	if signals.CompressionRatio != nil && *signals.CompressionRatio > thresholds.CompressionRatio {
		return true
	}
	return signals.NoSpeechProb != nil && signals.AvgLogprob != nil &&
		*signals.NoSpeechProb >= thresholds.NoSpeechProb && *signals.AvgLogprob < thresholds.AvgLogprob
}

// blankRuns — три и более переводов строки подряд схлопываются в один пустой.
var blankRuns = regexp.MustCompile(`\n{3,}`)

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func hasPrefix(list []string, value string) bool {
	for _, prefix := range list {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func anyContains(text string, parts []string) bool {
	for _, part := range parts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}
