// Package micuse — кто прямо сейчас разговаривает: встреча по захвату микрофона.
//
// Порт meetdigest_watcher/meetings.py.
//
// ОТКУДА ЗНАНИЕ. Windows сама ведёт учёт того, какое приложение и когда брало
// микрофон, — в ветке CapabilityAccessManager, той самой, что рисует красный
// значок в трее. У каждой записи есть время начала и время конца, и ноль в
// конце означает «держит прямо сейчас». Нам остаётся её прочитать.
//
// ПОЧЕМУ НЕ СПИСОК ПРОЦЕССОВ. Запущенный Телемост — ещё не встреча: он может
// висеть в трее неделю. Захваченный микрофон — уже встреча, потому что просто
// так его никто не берёт. Это разница между «программа открыта» и «человек
// говорит».
//
// ПОЧЕМУ НЕ КАЛЕНДАРЬ. Половина разговоров случается без приглашения:
// позвонили, собрались, обсудили. Календарь знает о запланированном,
// микрофон — о происходящем.
package micuse

import (
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

// consentPath — где Windows держит учёт обращений к микрофону.
const consentPath = `SOFTWARE\Microsoft\Windows\CurrentVersion\CapabilityAccessManager\ConsentStore\microphone\NonPackaged`

// Отсчёт Windows идёт с 1601 года в стонаносекундах.
const (
	filetimeEpochDiff = 11_644_473_600
	filetimeTicks     = 10_000_000
)

// KnownApps — приложения, для которых захват микрофона означает разговор с людьми.
//
// Ключ — конец пути к программе, значение — как называть встречу человеку.
var KnownApps = map[string]string{
	"yandextelemost.exe": "Телемост",
	"zoom.exe":           "Zoom",
	"ms-teams.exe":       "Teams",
	"teams.exe":          "Teams",
	"telegram.exe":       "Телеграм",
	"discord.exe":        "Discord",
	"skype.exe":          "Skype",
	"microsip.exe":       "звонок",
	"chrome.exe":         "встреча в Chrome",
	"msedge.exe":         "встреча в Edge",
	"browser.exe":        "встреча в Яндекс Браузере",
	"firefox.exe":        "встреча в Firefox",
}

// ignoredApps — программы, которые берут микрофон не ради разговора.
//
// Предлагать запись встречи посреди сведения звука в OBS — значит мешать
// работе, а не помогать.
var ignoredApps = map[string]bool{
	"obs64.exe": true, "obs32.exe": true, "splitcam.exe": true, "audacity.exe": true,
}

// Capture — одна запись из учёта Windows: кто брал микрофон и когда.
type Capture struct {
	Path      string
	StartedAt float64
	StoppedAt float64
}

// Program — имя исполняемого файла в нижнем регистре.
func (c Capture) Program() string {
	parts := strings.Split(c.Path, `\`)
	return strings.ToLower(parts[len(parts)-1])
}

// Running — ноль в конце значит, что захват не завершён: микрофон занят сейчас.
func (c Capture) Running() bool { return c.StoppedAt == 0 }

// Meeting — разговор, который идёт прямо сейчас.
type Meeting struct {
	Program   string
	Title     string
	StartedAt time.Time
	// Path — путь к программе; по нему берётся её значок для плашки.
	Path string
	// Tab, Host, Address — что открыто в браузере: по ним запрет попадает
	// точно в нужную встречу, а не в браузер целиком.
	Tab     string
	Host    string
	Address string
}

// Label — как встреча подписана на плашке.
//
// Название вкладки точнее имени программы: «встреча в Chrome» не отличает
// рабочую планёрку от вебинара, который слушают вполуха.
func (m Meeting) Label() string {
	if m.Tab != "" {
		return m.Tab
	}
	return m.Title
}

// Subject — чем адресуется решение «никогда не записывать».
func (m Meeting) Subject() string {
	if m.Host != "" {
		return m.Host
	}
	return m.Program
}

// ReadCaptures — прочитать учёт Windows.
func ReadCaptures() []Capture {
	root, err := registry.OpenKey(registry.CURRENT_USER, consentPath, registry.READ)
	if err != nil {
		// Ветки нет, пока микрофоном не пользовалась ни одна программа.
		return nil
	}
	defer func() { _ = root.Close() }()

	names, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}

	found := make([]Capture, 0, len(names))
	for _, name := range names {
		item, err := registry.OpenKey(root, name, registry.READ)
		if err != nil {
			continue
		}
		started, _, startErr := item.GetIntegerValue("LastUsedTimeStart")
		stopped, _, stopErr := item.GetIntegerValue("LastUsedTimeStop")
		_ = item.Close()
		if startErr != nil || stopErr != nil {
			continue
		}
		found = append(found, Capture{
			// Windows хранит путь с решётками вместо разделителей.
			Path:      strings.ReplaceAll(name, "#", `\`),
			StartedAt: float64(started),
			StoppedAt: float64(stopped),
		})
	}
	return found
}

// Look — идёт ли встреча прямо сейчас.
func Look() *Meeting {
	return Current(ReadCaptures(), LookAtBrowser)
}

// Current — идёт ли разговор и какой.
//
// Если микрофон держат сразу несколько программ (браузер и Телемост разом —
// обычное дело, когда ссылку открыли дважды), берём начатую последней: она и
// есть та встреча, куда человек в итоге зашёл.
//
// Чтение браузера приходит аргументом: оно стоит дорого (дерево доступности
// через COM), и проверкам его подменяют.
func Current(captures []Capture, browser func(program string) Page) *Meeting {
	var latest *Capture
	for index := range captures {
		one := captures[index]
		if !one.Running() {
			continue
		}
		program := one.Program()
		if _, known := KnownApps[program]; !known {
			continue
		}
		if ignoredApps[program] {
			continue
		}
		if latest == nil || one.StartedAt > latest.StartedAt {
			copied := one
			latest = &copied
		}
	}
	if latest == nil {
		return nil
	}

	program := latest.Program()
	made := &Meeting{
		Program:   program,
		Title:     KnownApps[program],
		StartedAt: time.Unix(int64(latest.StartedAt/filetimeTicks-filetimeEpochDiff), 0),
		Path:      latest.Path,
	}
	if browser != nil {
		page := browser(program)
		made.Tab = page.Title
		made.Host = page.Host()
		made.Address = page.Address()
	}
	return made
}

// Duration — сколько уже идёт разговор.
func (m Meeting) Duration() time.Duration { return time.Since(m.StartedAt) }
