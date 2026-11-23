// Package notes — история голосовых заметок.
//
// ЗАЧЕМ. Заметка голосом нужна не для того, чтобы один раз превратиться в текст
// и исчезнуть: человек хочет в любой момент посмотреть всё, что он когда-либо
// наговорил, и с какого устройства. Значит у заметки есть история, а у истории —
// место на диске.
//
// ПОЧЕМУ ФАЙЛ СТРОК, А НЕ БАЗА. Заметок за годы наберётся тысячи, не миллионы.
// Дописывание строки в конец файла не может испортить уже записанное — даже если
// свет выключат на середине, пострадает одна последняя строка, а вся история
// останется читаемой. База потребовала бы или CGO (и конца бесплатной
// кросс-сборке), или зависимости на пол-проекта, и всё это ради выборки, которая
// целиком влезает в память.
//
// РАСШИФРОВКА ОТДЕЛЕНА ОТ ЗАПИСИ. Заметка сохраняется сразу после того, как
// человек отпустил клавишу, ещё без текста: распознавание может не получиться
// (нет сети, кончился ключ), и тогда важно, чтобы остался хотя бы звук. Текст
// приезжает позже и дописывается отдельной строкой.
package notes

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

// Note — одна заметка.
type Note struct {
	ID string `json:"id"`
	// At — когда наговорили.
	At time.Time `json:"at"`
	// Seconds — длительность записи.
	Seconds float64 `json:"seconds"`
	// Device — с какого устройства: история общая, а устройств у человека
	// несколько, и «откуда это» — половина ответа на «что это было».
	Device string `json:"device"`
	// Source — откуда заметка пришла: из виджета или из Harness. История у обоих
	// клиентов одна, и по этому полю её фильтруют: диктовка в чужом окне и
	// заметка голосом — разные вещи, хотя и то и другое речь.
	Source string `json:"source,omitempty"`
	// Audio — путь к звуку. Остаётся даже когда текста нет.
	Audio string `json:"audio"`
	// Text — расшифровка. Пусто — ещё не расшифрована или не получилось.
	Text string `json:"text"`
	// Speech — чем расшифровано: чтобы потом было видно, чей это текст.
	//
	// Значение — имя способа из общего словаря (internal/speech: dictation,
	// local, gigaam, transcribe, api), и оно одно на обе половины диктовки:
	// значок и плагин Harness пишут в одну историю, и способ в ней обязан
	// читаться одинаково — иначе выборка по способу теряла бы половину записей.
	// В записях, сделанных до этого уговора, встречается прежнее слово значка
	// («диктовка») — читать его обязательно, см. settingsapi.speechTitle.
	Speech string `json:"speech,omitempty"`
	// Trouble — почему расшифровки нет. Человек должен видеть причину, а не
	// пустую строку.
	Trouble string `json:"trouble,omitempty"`
	// Sent — заметка ушла на сервис (для истории с других устройств).
	Sent bool `json:"sent,omitempty"`
}

// Откуда пришла заметка.
//
// Словарь закрытый: по этим словам фильтруют, и свой синоним в каждой программе
// развёл бы одну и ту же запись на две половины.
const (
	// SourceWidget — значок на этой машине: диктовка в чужое окно и заметка
	// голосом.
	SourceWidget = "widget"
	// SourceHarness — голосовой набор в Harness.
	SourceHarness = "harness"
)

// Short — первая строка текста: ею заметка подписана в списке.
func (n Note) Short() string {
	text := strings.TrimSpace(n.Text)
	if text == "" {
		if n.Trouble != "" {
			return "без расшифровки: " + n.Trouble
		}
		return "расшифровывается…"
	}
	if cut := strings.IndexAny(text, ".\n!?"); cut > 0 && cut < 80 {
		return text[:cut+1]
	}
	// Режем по границе рун, а не байт: иначе в подписи остаётся огрызок
	// кириллической буквы.
	runes := []rune(text)
	if len(runes) > 60 {
		return string(runes[:60]) + "…"
	}
	return text
}

// Store — история на диске.
type Store struct {
	path string
	mu   sync.Mutex
	// forgotten — что человек удалил. Держим отдельно, потому что история
	// только дописывается: удаление — это перепись файла без этих заметок.
	forgotten []string
}

// DefaultPath — где лежит история.
func DefaultPath(home string) string {
	return filepath.Join(home, "notes.jsonl")
}

// AudioFolder — где лежат записи заметок.
//
// Рядом с историей, а не в «Документах»: заметка на три секунды — не файл,
// который человек открывает руками, и в папке с записями встреч он только
// мешал бы.
func AudioFolder(home string) string {
	return filepath.Join(home, "notes")
}

func NewStore(path string) *Store { return &Store{path: path} }

// Add — записать заметку.
func (s *Store) Add(note Note) error {
	if note.ID == "" {
		note.ID = NewID(note.At)
	}
	return s.append(note)
}

// Update — дописать расшифровку (или причину её отсутствия).
//
// Дописыванием, а не перезаписью файла: так не теряется ничего, если процесс
// умрёт на середине, и так же работает любой журнал.
func (s *Store) Update(id string, change func(*Note)) error {
	s.mu.Lock()
	found, err := s.read()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	note, ok := found[id]
	if !ok {
		return fmt.Errorf("нет заметки %s", id)
	}
	change(&note)
	return s.append(note)
}

// List — история, свежие впереди.
func (s *Store) List(limit int) ([]Note, error) {
	s.mu.Lock()
	found, err := s.read()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}

	all := make([]Note, 0, len(found))
	for _, note := range found {
		all = append(all, note)
	}
	sort.Slice(all, func(one, other int) bool { return all[one].At.After(all[other].At) })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// Get — одна заметка.
func (s *Store) Get(id string) (Note, error) {
	s.mu.Lock()
	found, err := s.read()
	s.mu.Unlock()
	if err != nil {
		return Note{}, err
	}
	note, ok := found[id]
	if !ok {
		return Note{}, fmt.Errorf("нет заметки %s", id)
	}
	return note, nil
}

// Search — заметки, где встречается слово.
//
// Без регистра и по подстроке: человек ищет «созвон», а говорил «созвоны».
func (s *Store) Search(word string, limit int) ([]Note, error) {
	all, err := s.List(0)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(word))
	if needle == "" {
		if limit > 0 && len(all) > limit {
			return all[:limit], nil
		}
		return all, nil
	}

	found := make([]Note, 0, 8)
	for _, note := range all {
		if strings.Contains(strings.ToLower(note.Text), needle) {
			found = append(found, note)
			if limit > 0 && len(found) >= limit {
				break
			}
		}
	}
	return found, nil
}

// BySource — заметки одного источника: виджета или Harness.
//
// История у обоих клиентов одна, и «покажи только то, что я наговорил в Harness»
// — обычная просьба: диктовка в чужом окне и заметка голосом ищутся по-разному.
// Пустое слово — вся история: у заметок, записанных до появления признака, он не
// заполнен, и прятать их нельзя.
func (s *Store) BySource(source string, limit int) ([]Note, error) {
	all, err := s.List(0)
	if err != nil {
		return nil, err
	}
	wanted := strings.TrimSpace(source)
	found := make([]Note, 0, len(all))
	for _, note := range all {
		if wanted != "" && !strings.EqualFold(note.Source, wanted) {
			continue
		}
		found = append(found, note)
		if limit > 0 && len(found) >= limit {
			break
		}
	}
	return found, nil
}

// Forget — убрать заметку и её звук.
func (s *Store) Forget(id string) error {
	note, err := s.Get(id)
	if err != nil {
		return err
	}

	// Сначала история, потом файл со звуком: не удалится файл — в истории
	// заметки уже нет, и человек видит то, что просил. Наоборот было бы хуже:
	// строка в истории, ведущая в пустоту.
	s.mu.Lock()
	s.forgotten = append(s.forgotten, id)
	err = s.rewrite()
	s.mu.Unlock()
	if err != nil {
		return err
	}

	if note.Audio != "" {
		_ = os.Remove(note.Audio)
	}
	return nil
}

// read — собрать историю: последняя строка про заметку главнее прежних.
func (s *Store) read() (map[string]Note, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Note{}, nil
		}
		return nil, fmt.Errorf("прочитать историю: %w", err)
	}

	found := map[string]Note{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var note Note
		if err := json.Unmarshal([]byte(line), &note); err != nil {
			// Одна испорченная строка не повод потерять всю историю: пропускаем
			// её и читаем остальные.
			continue
		}
		if note.ID == "" {
			continue
		}
		found[note.ID] = note
	}
	for _, id := range s.forgotten {
		delete(found, id)
	}
	return found, nil
}

// append — дописать строку в конец.
func (s *Store) append(note Note) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("завести папку истории: %w", err)
	}
	raw, err := json.Marshal(note)
	if err != nil {
		return fmt.Errorf("сложить заметку: %w", err)
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("открыть историю: %w", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("записать заметку: %w", err)
	}
	return nil
}

// rewrite — перезаписать файл без выкинутых заметок.
//
// Через временный файл и переименование: прерванная перезапись не должна
// оставить половину истории.
func (s *Store) rewrite() error {
	found, err := s.read()
	if err != nil {
		return err
	}
	all := make([]Note, 0, len(found))
	for _, note := range found {
		all = append(all, note)
	}
	sort.Slice(all, func(one, other int) bool { return all[one].At.Before(all[other].At) })

	builder := &strings.Builder{}
	for _, note := range all {
		raw, err := json.Marshal(note)
		if err != nil {
			return err
		}
		builder.Write(raw)
		builder.WriteByte('\n')
	}

	temporary := s.path + ".new"
	if err := os.WriteFile(temporary, []byte(builder.String()), 0o600); err != nil {
		return fmt.Errorf("записать историю: %w", err)
	}
	return os.Rename(temporary, s.path)
}

// NewID — имя заметки: по времени, чтобы файлы на диске шли по порядку.
func NewID(at time.Time) string {
	if at.IsZero() {
		at = time.Now()
	}
	return at.Format("20060102-150405.000")
}
