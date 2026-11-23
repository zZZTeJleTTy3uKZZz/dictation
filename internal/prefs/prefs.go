// Package prefs — что человек решил про запись разговоров.
//
// Порт meetdigest_watcher/preferences.py, файл тот же (~/.meetdigest/catcher.json)
// и с тем же составом полей: обе версии воркера читают одни настройки, и
// переключение не теряет решений.
//
// ТРИ ВИДА РЕШЕНИЙ. «Никогда» — приложение больше не считается встречей
// (звонки родным, диктофон). «Всегда» — запись начинается сама, без вопроса.
// «Тишина» — не спрашивать до такого-то времени (сегодня не до того).
package prefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/atomicfile"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/appdirs"
)

// QuietFor — сколько длится тишина после «не спрашивать час».
const QuietFor = time.Hour

// Modes — что делать со встречами: название и объяснение словом, а не догадкой.
//
// Порядок — от самого разговорчивого к молчанию; им же идёт выбор в настройках.
var Modes = []struct {
	Key, Title, Note string
}{
	{"all", "Записывать всё", "любой разговор пишется сразу, без вопроса"},
	{"blacklist", "Спрашивать", "предлагаю запись везде, кроме запрещённого"},
	{"whitelist", "Только выбранное", "предлагаю только там, где сказано «всегда»"},
	{"off", "Не следить", "микрофон не отслеживается, вопросов не будет"},
}

// Mode — название и объяснение режима по ключу.
//
// Порядок в Modes — часть смысла (от разговорчивого к молчанию), поэтому это
// список, а поиск по нему — отдельной вспомогательной функцией.
func Mode(key string) (title, note string) {
	for _, mode := range Modes {
		if mode.Key == key {
			return mode.Title, mode.Note
		}
	}
	return key, ""
}

func knownMode(key string) bool {
	for _, mode := range Modes {
		if mode.Key == key {
			return true
		}
	}
	return false
}

// Prefs — решения человека.
type Prefs struct {
	Path string `json:"-"`

	Never  []string `json:"never"`
	Always []string `json:"always"`
	// NeverTabs — устаревшее, но читается: название вкладки меняется само
	// (счётчик сообщений, имя докладчика), и запрет по нему отваливался молча.
	NeverTabs   []string `json:"never_tabs"`
	NeverSites  []string `json:"never_sites"`
	NeverPages  []string `json:"never_pages"`
	AlwaysSites []string `json:"always_sites"`
	AlwaysPages []string `json:"always_pages"`
	QuietUntil  float64  `json:"quiet_until"`
	Mode        string   `json:"mode"`
}

func DefaultPath() string {
	home := appdirs.HomeDir()
	if home == "" {
		return "catcher.json"
	}
	return filepath.Join(home, ".meetdigest", "catcher.json")
}

// Load — прочитать решения.
//
// Испорченный файл — не повод переставать ловить встречи: решения человек
// переназначит, а молчащий помощник он заметит не сразу.
func Load(path string) *Prefs {
	if path == "" {
		path = DefaultPath()
	}
	made := &Prefs{Path: path, Mode: "blacklist"}
	raw, err := os.ReadFile(path)
	if err != nil {
		return made
	}
	var stored Prefs
	if err := json.Unmarshal(raw, &stored); err != nil {
		return made
	}
	stored.Path = path
	if !knownMode(stored.Mode) {
		// Незнакомый режим из файла — не повод молчать или писать всё подряд.
		stored.Mode = "blacklist"
	}
	stored.lower()
	return &stored
}

func (p *Prefs) lower() {
	for _, list := range [][]string{
		p.Never, p.Always, p.NeverTabs, p.NeverSites, p.NeverPages,
		p.AlwaysSites, p.AlwaysPages,
	} {
		for index, value := range list {
			list[index] = strings.ToLower(strings.TrimSpace(value))
		}
	}
}

func (p *Prefs) Save() error {
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
		return err
	}
	for _, list := range [][]string{
		p.Never, p.Always, p.NeverTabs, p.NeverSites, p.NeverPages,
		p.AlwaysSites, p.AlwaysPages,
	} {
		sort.Strings(list)
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.Path, append(raw, '\n'), 0o644)
}

// ── решения ───────────────────────────────────────────────────────────────

// Forbid — никогда не записывать это приложение.
func (p *Prefs) Forbid(program string) {
	program = key(program)
	p.Never = add(p.Never, program)
	// Два противоположных решения разом невозможны.
	p.Always = drop(p.Always, program)
}

// AllowAlways — записывать это приложение сразу, не спрашивая.
func (p *Prefs) AllowAlways(program string) {
	program = key(program)
	p.Always = add(p.Always, program)
	p.Never = drop(p.Never, program)
}

// Forget — забыть решение: снова спрашивать, как в первый раз.
func (p *Prefs) Forget(program string) {
	program = key(program)
	p.Never = drop(p.Never, program)
	p.Always = drop(p.Always, program)
}

func (p *Prefs) ForbidSite(host string) {
	p.NeverSites = add(p.NeverSites, key(host))
	p.AlwaysSites = drop(p.AlwaysSites, key(host))
}
func (p *Prefs) ForbidPage(page string) {
	p.NeverPages = add(p.NeverPages, key(page))
	p.AlwaysPages = drop(p.AlwaysPages, key(page))
}
func (p *Prefs) AllowSite(host string) {
	p.AlwaysSites = add(p.AlwaysSites, key(host))
	p.NeverSites = drop(p.NeverSites, key(host))
}
func (p *Prefs) AllowPage(page string) {
	p.AlwaysPages = add(p.AlwaysPages, key(page))
	p.NeverPages = drop(p.NeverPages, key(page))
}

// BeQuiet — помолчать: не предлагать записи до истечения срока.
func (p *Prefs) BeQuiet(now time.Time) { p.QuietUntil = float64(now.Add(QuietFor).Unix()) }

// Speak — снять тишину досрочно.
func (p *Prefs) Speak() { p.QuietUntil = 0 }

func (p *Prefs) SetMode(mode string) {
	if knownMode(mode) {
		p.Mode = mode
		return
	}
	p.Mode = "blacklist"
}

// ── вопросы ───────────────────────────────────────────────────────────────

func (p *Prefs) Forbids(program string) bool  { return has(p.Never, key(program)) }
func (p *Prefs) ForbidsTab(tab string) bool   { return tab != "" && has(p.NeverTabs, key(tab)) }
func (p *Prefs) ForbidsSite(host string) bool { return host != "" && has(p.NeverSites, key(host)) }
func (p *Prefs) ForbidsPage(page string) bool { return page != "" && has(p.NeverPages, key(page)) }
func (p *Prefs) AllowsSite(host string) bool  { return host != "" && has(p.AlwaysSites, key(host)) }
func (p *Prefs) AllowsPage(page string) bool  { return page != "" && has(p.AlwaysPages, key(page)) }

// Whitelist — режим «только выбранное».
func (p *Prefs) Whitelist() bool { return p.Mode == "whitelist" }

// Catching — следим ли за микрофоном вообще.
func (p *Prefs) Catching() bool { return p.Mode != "off" }

// Silent — попросили помолчать.
func (p *Prefs) Silent(now time.Time) bool { return float64(now.Unix()) < p.QuietUntil }

// Demands — начинать ли запись сразу, не спрашивая.
//
// В режиме «записывать всё» — да для любого разговора, кроме прямо
// запрещённого: «всё» касается встреч, а не звонков родным, которые человек
// однажды уже вычеркнул.
//
// В остальных режимах разрешение может быть выдано трём разным вещам:
// программе (Телемост, Zoom), сайту целиком или одной переговорной. Для
// браузера годятся только последние два: разрешив Chrome, человек разрешил бы
// заодно музыку и видео — всё, что берёт там микрофон.
func (p *Prefs) Demands(program, host, page string) bool {
	if p.Mode == "all" {
		return !p.Forbids(program)
	}
	if has(p.Always, key(program)) {
		return true
	}
	return p.AllowsSite(host) || p.AllowsPage(page)
}

// Offers — предлагать ли запись для этой встречи.
func (p *Prefs) Offers(program, tab, host, page string) bool {
	if !p.Catching() {
		return false
	}
	if p.Demands(program, host, page) {
		return true
	}
	if p.Whitelist() {
		return false
	}
	// Запрет страницы бьёт точнее запрета сайта, а тот — точнее запрета
	// программы; проверяются все три, потому что запретить можно любым.
	return !p.Forbids(program) && !p.ForbidsTab(tab) &&
		!p.ForbidsSite(host) && !p.ForbidsPage(page)
}

// ── мелочи ────────────────────────────────────────────────────────────────

func key(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func has(list []string, value string) bool {
	for _, one := range list {
		if one == value {
			return true
		}
	}
	return false
}

func add(list []string, value string) []string {
	if value == "" || has(list, value) {
		return list
	}
	return append(list, value)
}

func drop(list []string, value string) []string {
	kept := make([]string, 0, len(list))
	for _, one := range list {
		if one != value {
			kept = append(kept, one)
		}
	}
	return kept
}

// ── как показывать запись ─────────────────────────────────────────────────

// Display — чем показывать идущую запись.
//
// ДВА СПОСОБА, И ОНИ НЕ ЗАМЕНЯЮТ ДРУГ ДРУГА. Окно с таймером видно, не отводя
// глаз, — но оно занимает угол экрана всю встречу. Красный значок в лотке не
// мешает вовсе, но требует туда посмотреть. Выключить оба можно, и это
// осознанный выбор; по умолчанию включены оба — молча писать разговор нехорошо.
type Display struct {
	Path   string `json:"-"`
	Window bool   `json:"window"`
	Tray   bool   `json:"tray"`
}

func DisplayPath() string {
	home := appdirs.HomeDir()
	if home == "" {
		return "display.json"
	}
	return filepath.Join(home, ".meetdigest", "display.json")
}

func LoadDisplay(path string) *Display {
	if path == "" {
		path = DisplayPath()
	}
	made := &Display{Path: path, Window: true, Tray: true}
	raw, err := os.ReadFile(path)
	if err != nil {
		return made
	}
	var stored struct {
		Window *bool `json:"window"`
		Tray   *bool `json:"tray"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return made
	}
	if stored.Window != nil {
		made.Window = *stored.Window
	}
	if stored.Tray != nil {
		made.Tray = *stored.Tray
	}
	return made
}

func (d *Display) Save() error {
	if err := os.MkdirAll(filepath.Dir(d.Path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(struct {
		Window bool `json:"window"`
		Tray   bool `json:"tray"`
	}{d.Window, d.Tray}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(d.Path, append(raw, '\n'), 0o644)
}

// ── как писать ────────────────────────────────────────────────────────────

// Quality — качество записи: что это значит на слух и по весу часа разговора.
//
// Распознаватель всё равно сводит звук к 16 кГц, поэтому «выше» здесь — не про
// чистоту речи, а про запас: на плохом микрофоне и в шумной комнате более
// плотный поток теряет меньше.
var Quality = map[string][3]string{
	"low":    {"Экономно", "32 кбит/с · час ≈ 14 МБ", "32k"},
	"normal": {"Обычное", "48 кбит/с · час ≈ 21 МБ", "48k"},
	"high":   {"С запасом", "80 кбит/с · час ≈ 35 МБ", "80k"},
}

// QualityOrder — порядок качества в настройках: от экономного к запасу.
//
// Список рядом с картой, потому что обход карты в Go случаен, и выбор в окне
// перетряхивался бы при каждом открытии.
var QualityOrder = []string{"low", "normal", "high"}

// ScreenQuality — насколько подробно писать экран.
//
// «HD» ужимает вчетверо и остаётся читаемым: разбор встречи 07.09.2026 делался
// по кадрам 1536 в ширину, и мелкий шрифт CRM читался весь.
var ScreenQuality = map[string][3]string{
	"hd":   {"HD", "1280 точек в ширину · час ≈ 400 МБ", "1280"},
	"full": {"Подробно", "1920 точек в ширину · час ≈ 1 ГБ", "1920"},
}

// ScreenQualityOrder — порядок подробности экрана.
var ScreenQualityOrder = []string{"hd", "full"}

// Recording — как и куда писать разговоры.
type Recording struct {
	Path    string `json:"-"`
	Quality string `json:"quality"`
	Folder  string `json:"folder"`
	// Screen — писать ли заодно экран. Выключено по умолчанию: смотреть в
	// разговоре чаще всего не на что, а видео весит сотни мегабайт и занимает
	// процессор — ровно то, из-за чего 07.09.2026 у записи Телемоста пропал звук.
	Screen        bool   `json:"screen"`
	ScreenQuality string `json:"screen_quality"`
	// ScreenTarget — «all» или номер экрана. На двух мониторах «весь стол» —
	// это полоса, где половина кадра занята посторонним.
	ScreenTarget string `json:"screen_target"`
}

func RecordingPath() string {
	home := appdirs.HomeDir()
	if home == "" {
		return "recording.json"
	}
	return filepath.Join(home, ".meetdigest", "recording.json")
}

// DefaultFolder — куда сервис пишет разговоры по умолчанию.
//
// Своя папка, а не та, за которой идёт обход: в папке программы созвонов лежат
// её собственные записи, и складывать туда ещё и свои — значит смешать два
// потока файлов и однажды не понять, что чьё.
func DefaultFolder() string {
	home := appdirs.HomeDir()
	if home == "" {
		return "MeetDigest"
	}
	return filepath.Join(home, "Documents", "MeetDigest")
}

func LoadRecording(path string) *Recording {
	if path == "" {
		path = RecordingPath()
	}
	made := &Recording{
		Path: path, Quality: "normal", ScreenQuality: "hd", ScreenTarget: "all",
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return made
	}
	var stored Recording
	if err := json.Unmarshal(raw, &stored); err != nil {
		return made
	}
	stored.Path = path
	if _, ok := Quality[stored.Quality]; !ok {
		stored.Quality = "normal"
	}
	if _, ok := ScreenQuality[stored.ScreenQuality]; !ok {
		stored.ScreenQuality = "hd"
	}
	if stored.ScreenTarget == "" {
		stored.ScreenTarget = "all"
	}
	return &stored
}

func (r *Recording) Save() error {
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.Path, append(raw, '\n'), 0o644)
}

// Bitrate — плотность звука для выбранного качества.
func (r *Recording) Bitrate() string { return Quality[r.Quality][2] }

// ScreenWidth — до какой ширины ужимать картинку экрана.
func (r *Recording) ScreenWidth() string { return ScreenQuality[r.ScreenQuality][2] }

// Directory — папка записей: выбранная или своя по умолчанию.
func (r *Recording) Directory() string {
	if strings.TrimSpace(r.Folder) == "" {
		return DefaultFolder()
	}
	if strings.HasPrefix(r.Folder, "~") {
		if home := appdirs.HomeDir(); home != "" {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(r.Folder, "~"), "/"))
		}
	}
	return r.Folder
}

// Target — куда класть готовую запись. Папка заводится, если её ещё нет.
func (r *Recording) Target() (string, error) {
	chosen := r.Directory()
	if err := os.MkdirAll(chosen, 0o755); err != nil {
		return "", err
	}
	return chosen, nil
}

// ── диктофон ──────────────────────────────────────────────────────────────

// Voice — настройки заметок голосом.
//
// Отдельным файлом от записи встреч: это другая работа с другими решениями.
// Человек может писать встречи «с запасом», а заметки — экономно, и наоборот.
type Voice struct {
	Path string `json:"-"`
	// On — включён ли диктофон вовсе.
	On bool `json:"on"`
	// Hotkey — сочетание клавиш, как его написал человек.
	Hotkey string `json:"hotkey"`
	// Speech — чем расшифровывать: имя способа или «auto».
	Speech string `json:"speech"`
	// SpeechURL, SpeechKey, SpeechModel — для способа «по сети».
	SpeechURL   string `json:"speech_url"`
	SpeechKey   string `json:"speech_key"`
	SpeechModel string `json:"speech_model"`
	// SpeechLanguage — на каком языке речь. Пусто — русский: другой язык здесь
	// никто не диктует, а распознавателю пустое поле означает «угадывай сам»,
	// и короткая фраза уезжает в английский.
	SpeechLanguage string `json:"speech_language"`
	// Keep — сколько дней держать звук заметок.
	//
	// Ноль — держать всегда. Звук нужен, пока текст не проверен; дальше он
	// занимает место, а ценность уже в тексте.
	Keep int `json:"keep_days"`
}

func VoicePath() string {
	home := appdirs.HomeDir()
	if home == "" {
		return "voice.json"
	}
	return filepath.Join(home, ".meetdigest", "voice.json")
}

// LoadVoice — прочитать настройки диктофона.
//
// Сочетание по умолчанию подставляется здесь, а не в диктофоне: так человек
// видит в настройках то самое сочетание, которое работает, даже если он его
// никогда не трогал.
func LoadVoice(path string) *Voice {
	if path == "" {
		path = VoicePath()
	}
	made := &Voice{Path: path, On: true, Hotkey: DefaultHotkey, Speech: "auto"}
	raw, err := os.ReadFile(path)
	if err != nil {
		return made
	}
	var stored struct {
		On          *bool  `json:"on"`
		Hotkey      string `json:"hotkey"`
		Speech      string `json:"speech"`
		SpeechURL   string `json:"speech_url"`
		SpeechKey   string `json:"speech_key"`
		SpeechModel string `json:"speech_model"`
		// Поля, которых в прежних файлах не было, читаются пустыми: прежние
		// значения способа остаются как были, а новое берёт умолчание.
		SpeechLanguage string `json:"speech_language"`
		Keep           int    `json:"keep_days"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		// Испорченный файл — не повод лишить человека диктофона: настройки он
		// переназначит, а неработающую клавишу заметит не сразу.
		return made
	}
	if stored.On != nil {
		made.On = *stored.On
	}
	made.Hotkey = MigrateHotkey(stored.Hotkey)
	if strings.TrimSpace(stored.Speech) != "" {
		made.Speech = strings.TrimSpace(stored.Speech)
	}
	made.SpeechURL = stored.SpeechURL
	made.SpeechKey = stored.SpeechKey
	made.SpeechModel = stored.SpeechModel
	made.SpeechLanguage = stored.SpeechLanguage
	made.Keep = stored.Keep
	return made
}

func (v *Voice) Save() error {
	if err := os.MkdirAll(filepath.Dir(v.Path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// 0600, а не 0644: ключ к сервису распознавания здесь ещё бывает —
	// старый, до переноса в хранилище.
	//
	// Запись атомарная: файл читают значок, служба и ядро, а пишут и окно, и
	// миграция ключа. Обычная запись оставляет читателю пустой или обрезанный
	// файл — и диктовка на миг видит «ничего не настроено», а миграция,
	// прочитав пустое, могла бы решить, что ключа нет.
	return atomicfile.Write(v.Path, append(raw, '\n'), 0o600)
}

// DefaultHotkey — сочетание по умолчанию.
//
// Повторено здесь строкой, а не взято из пакета hotkey: настройки не должны
// зависеть от того, кто умеет регистрировать клавиши, иначе окно настроек
// потянуло бы за собой работу с системой.
const DefaultHotkey = "Ctrl+Space"

// legacyHotkey — прежнее умолчание, которое не означает выбора человека.
//
// Ctrl+D стоял по умолчанию до переезда на пробел. Windows отдаёт
// зарегистрированное сочетание нам и до окна его не доносит, а в агентской
// оболочке Ctrl+D запускает диктовку в самом поле ввода — то есть прежнее
// умолчание отнимало клавишу у страницы.
const legacyHotkey = "Ctrl+D"

// MigrateHotkey — заменить прежнее умолчание нынешним, не трогая выбор человека.
//
// Пустое значит «сочетание не трогали», Ctrl+D — прежнее умолчание: и то и
// другое молча становится Ctrl+Space. Любое другое сохранённое сочетание
// остаётся как есть, даже если совпадает с каким-нибудь прежним умолчанием:
// отличить выбранное от умолчания по файлу нельзя, и переписывать чужой выбор
// хуже, чем оставить его.
func MigrateHotkey(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.EqualFold(trimmed, legacyHotkey) {
		return DefaultHotkey
	}
	return trimmed
}
