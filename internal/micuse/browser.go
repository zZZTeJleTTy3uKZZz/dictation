// Что за встреча идёт в браузере: вкладка и сайт.
//
// Порт meetdigest_watcher/browser.py.
//
// ЗАЧЕМ. «Встреча в Chrome» — почти бесполезная подпись: в браузере проходят и
// рабочая планёрка, и урок ребёнка, и вебинар, который слушают вполуха.
// Название вкладки и сайт превращают вопрос «записать?» в осмысленный: видно,
// что именно предлагают записать.
//
// И это же делает запреты точными. Запретить браузер целиком — грубо: тогда
// пропадут и рабочие созвоны. Запрет по сайту («никогда не записывать
// meet.google.com») и по странице («эту переговорную — никогда») попадает ровно
// туда, куда нужно.
//
// ОТКУДА БЕРЁТСЯ. Заголовок окна — у системы. Адрес — из самого браузера: он
// показывает адресную строку как обычный элемент интерфейса, и Windows умеет
// её прочитать тем же способом, каким это делают программы для незрячих.
package micuse

import (
	"net/url"
	"strings"
	"syscall"
	"unsafe"
)

// browserSuffixes — как в заголовке окна отмечен сам браузер.
//
// Это отрезается, остаётся название вкладки.
var browserSuffixes = []string{
	" - Google Chrome", " — Google Chrome",
	" - Microsoft\u200b Edge", " - Microsoft Edge", " — Microsoft Edge",
	" - Яндекс Браузер", " — Яндекс Браузер", " - Yandex",
	" — Mozilla Firefox", " - Mozilla Firefox",
}

var browsers = map[string]bool{
	"chrome.exe": true, "msedge.exe": true, "browser.exe": true,
	"firefox.exe": true, "opera.exe": true, "vivaldi.exe": true,
}

// Page — открытая страница: что видно человеку и чем это адресуется.
type Page struct {
	Title string
	URL   string
}

// Host — сайт без «www»: им и адресуется запрет «никогда не записывать».
func (p Page) Host() string {
	if p.URL == "" {
		return ""
	}
	parsed, err := url.Parse(p.URL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")
}

// Address — адрес страницы без хвоста запроса.
//
// Ссылки на встречу и так уникальны, а метки вроде ?utm= меняются от письма к
// письму — по ним одна и та же переговорная выглядела бы каждый раз новой.
func (p Page) Address() string {
	if p.URL == "" {
		return ""
	}
	parsed, err := url.Parse(p.URL)
	if err != nil {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.ToLower(parsed.String())
}

// LookAtBrowser — что открыто в браузере, который держит микрофон.
//
// Пусто для всего, что браузером не является: у Телемоста и Zoom нет ни
// вкладок, ни адресов.
func LookAtBrowser(program string) Page {
	if !browsers[strings.ToLower(program)] {
		return Page{}
	}
	title := activeWindowTitle(program)
	return Page{Title: trimBrowserName(title)}
}

// trimBrowserName — убрать из заголовка имя браузера.
func trimBrowserName(title string) string {
	for _, suffix := range browserSuffixes {
		if strings.HasSuffix(title, suffix) {
			return strings.TrimSpace(strings.TrimSuffix(title, suffix))
		}
	}
	return strings.TrimSpace(title)
}

// activeWindowTitle — заголовок окна, принадлежащего этой программе.
//
// Берём переднее окно: у браузера окон бывает много, но микрофон держит то, в
// которое человек смотрит, — остальные ему не нужны.
func activeWindowTitle(program string) string {
	user32 := syscall.NewLazyDLL("user32.dll")
	foreground := user32.NewProc("GetForegroundWindow")
	getText := user32.NewProc("GetWindowTextW")
	getProcess := user32.NewProc("GetWindowThreadProcessId")

	handle, _, _ := foreground.Call()
	if handle == 0 {
		return ""
	}

	var pid uint32
	_, _, _ = getProcess.Call(handle, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 || !sameProgram(pid, program) {
		// Переднее окно — не этой программы: заголовок чужой, и подставлять
		// его значило бы подписать встречу названием случайного окна.
		return ""
	}

	buffer := make([]uint16, 512)
	length, _, _ := getText.Call(handle, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if length == 0 {
		return ""
	}
	return syscall.UTF16ToString(buffer[:length])
}

// sameProgram — это окно принадлежит нужной программе?
func sameProgram(pid uint32, program string) bool {
	const access = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	open := kernel32.NewProc("OpenProcess")
	name := kernel32.NewProc("QueryFullProcessImageNameW")
	closeHandle := kernel32.NewProc("CloseHandle")

	handle, _, _ := open.Call(access, 0, uintptr(pid))
	if handle == 0 {
		return false
	}
	defer func() { _, _, _ = closeHandle.Call(handle) }()

	buffer := make([]uint16, 1024)
	size := uint32(len(buffer))
	ok, _, _ := name.Call(handle, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return false
	}
	full := syscall.UTF16ToString(buffer[:size])
	parts := strings.Split(full, `\`)
	return strings.EqualFold(parts[len(parts)-1], program)
}
