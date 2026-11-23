// Словарь терминов человека — подсказка распознавателю.
//
// ЗАЧЕМ ПОДСКАЗКА. Замеры: без неё whisper пишет
// «лидеруб» строчными и без знаков препинания, с ней — «Лидоруб» и с разбивкой на
// предложения. Подсказка едет тем же запросом, что и звук, полем prompt.
//
// ОТКУДА СЛОВАРЬ. Из providers.json — из того же поля, которым пользуется
// причёсывание: словарь у человека один, и второй список терминов разошёлся бы с
// первым на первой же правке.
//
// ПОЧЕМУ ПЕРЕЧИТЫВАЕТСЯ. Словарь растёт из его же диктовок, а правят его в
// другом окне. Файл перечитывается по времени правки и размеру — тем же приёмом,
// что у службы диктовки (internal/localdict/reload.go), — поэтому новый термин
// работает со следующей заметки, без перезапуска значка и без перезапуска
// службы. Битый или недописанный файл оставляет прежний словарь: правка в
// полстроки не должна оставить человека без подсказки вовсе.
package speech

import (
	"os"
	"strings"
	"sync"
	"unicode"
)

// hintPrefix — что говорится распознавателю перед словарём.
//
// Просьба сформулирована как указание о письме, а не как «вот слова»: модели,
// которой просто выдали список, хватает повода переписать фразу целиком.
const hintPrefix = "Расставь знаки препинания и заглавные буквы. Имена и термины пиши так: "

// hintLimit — сколько знаков подсказки пускать в запрос.
//
// У whisper подсказка ограничена двумя сотнями с небольшим кусочков, а кусочек —
// это примерно пара знаков русского текста. Словарь в шестьдесят с лишним
// терминов вместе с просьбой в предел не влезает, и хвост подсказки сервис
// отбрасывает молча. Обрезаем сами и по границе слова: обрубок последнего
// термина модель достроит как захочет.
const hintLimit = 400

// fileStamp — отпечаток файла настроек: время правки и размер.
//
// По нему видно, что файл изменили, и разбирать его на каждой заметке не надо.
type fileStamp struct {
	modified int64
	size     int64
	present  bool
}

// stampOf — отпечаток файла. Файла нет — пустой отпечаток: удаление словаря
// тоже правка, и заметить её надо.
func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{modified: info.ModTime().UnixNano(), size: info.Size(), present: true}
}

// Glossary — словарь терминов, каким он был прочитан последний раз.
type Glossary struct {
	// Path — где providers.json; пусто — обычное место службы диктовки.
	Path string

	mu      sync.Mutex
	watched fileStamp
	terms   string
	hint    string
}

func NewGlossary(path string) *Glossary { return &Glossary{Path: path} }

// Hint — подсказка распознавателю целиком: просьба и словарь. Пусто — словаря
// нет, и тогда поле prompt в запрос не идёт вовсе.
func (g *Glossary) Hint() string {
	g.mu.Lock()
	defer g.mu.Unlock()

	path := g.Path
	if path == "" {
		path = ProvidersPath()
	}
	stamp := stampOf(path)
	if stamp == g.watched {
		return g.hint
	}
	// Отпечаток запоминается и при неудаче: о битом файле незачем читать снова и
	// снова на каждой заметке — прежний словарь работает, пока файл не поправят.
	g.watched = stamp
	settings, ok := ReadSettings(path)
	if !ok {
		return g.hint
	}
	g.terms = strings.Join(strings.Fields(settings.Config.Glossary), " ")
	g.hint = hintOf(g.terms)
	return g.hint
}

// Terms — сам словарь, без просьбы. Нужен, чтобы показать человеку, чем
// распознавание подсказывают, и чтобы это можно было проверить.
func (g *Glossary) Terms() string {
	g.Hint()
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.terms
}

// hintOf — собрать подсказку из словаря.
func hintOf(terms string) string {
	terms = strings.Join(strings.Fields(terms), " ")
	if terms == "" {
		return ""
	}
	whole := hintPrefix + terms
	if len([]rune(whole)) <= hintLimit {
		return whole
	}
	// Режем по границе слова, а не по знаку: обрубок термина модель достроит как
	// захочет, и в тексте появится слово, которого человек не говорил.
	cut := string([]rune(whole)[:hintLimit])
	if at := strings.LastIndexFunc(cut, unicode.IsSpace); at > 0 {
		cut = cut[:at]
	}
	return strings.TrimRightFunc(cut, func(r rune) bool {
		return r == ',' || r == ';' || r == '.' || unicode.IsSpace(r)
	})
}
