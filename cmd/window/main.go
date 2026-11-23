// Окно клиента: настройки, заметки и плашка встречи.
//
// ПОЧЕМУ ОТДЕЛЬНАЯ ПРОГРАММА. У окна свой цикл сообщений, и живёт оно минуту, а
// наблюдение — сутками. Упади окно (а движок браузера умеет не запуститься на
// свежей машине) — воркер не должен этого заметить: он просто не получит ответа
// и сочтёт это отказом.
//
// ПОЧЕМУ ЗДЕСЬ НЕТ ПРАВИЛ. Всё, что окно знает о настройках, оно спрашивает у
// internal/settingsapi — того же места, куда ходит окно на Flutter и куда пойдёт
// любое следующее. Раньше здесь лежала своя копия этих правил, и она уже успела
// разойтись с ядром: два разных решения по Teams выглядели как одна строка,
// повторённая дважды.
//
// КАК ОТВЕЧАЕТ ПЛАШКА. Одной строкой в стандартный вывод: "record", "always",
// "never", "quiet" или пусто. Канал нарочно простейший — его читает тот, кто
// запустил процесс, и ничего настраивать не нужно.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	webview "github.com/jchv/go-webview2"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/settingsapi"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/ui"
)

func main() {
	kind := flag.String("kind", "settings", "какое окно: settings, notes или ask")
	title := flag.String("title", "", "название встречи (для плашки)")
	program := flag.String("program", "", "чем идёт встреча")
	host := flag.String("host", "", "сайт встречи")
	flag.Parse()

	switch *kind {
	case "ask":
		fmt.Println(askWindow(*title, *program, *host))
	case "notes":
		notesWindow()
	default:
		settingsWindow()
	}
}

// ── плашка встречи ────────────────────────────────────────────────────────

func askWindow(title, program, host string) string {
	answer := ""
	window := webview.NewWithOptions(webview.WebViewOptions{
		AutoFocus: true,
		WindowOptions: webview.WindowOptions{
			Title: "Записать встречу?", Width: 420, Height: 210, Center: true,
		},
	})
	if window == nil {
		// Движок браузера не поднялся — спросить нечем. Пустой ответ означает
		// отказ: молча писать разговор нельзя.
		return ""
	}
	defer window.Destroy()

	_ = window.Bind("load", func() map[string]string {
		return map[string]string{"title": title, "program": program, "host": host}
	})
	_ = window.Bind("answer", func(value string) {
		answer = value
		window.Terminate()
	})
	window.SetHtml(ui.Page("ask"))
	window.Run()
	return answer
}

// ── настройки ─────────────────────────────────────────────────────────────

func settingsWindow() {
	window := open("Настройки MeetDigest", 780, 860)
	defer window.Destroy()

	// Страница сообщает о себе в поток ошибок: иначе «белое окно» и «окно не
	// открылось» выглядят одинаково, а причины у них разные.
	_ = window.Bind("load", func() settingsapi.View {
		fmt.Fprintln(os.Stderr, "страница настроек загрузилась")
		return settingsapi.Read()
	})
	_ = window.Bind("closeWindow", window.Terminate)

	// Каждая правка сразу уходит в ядро и сразу ложится в файл: человек
	// закрывает окно крестиком, и кнопку «сохранить» он искать не будет.
	for _, action := range []string{
		"telemost", "remove-folder", "window", "tray",
		"quality", "screen", "screen-quality", "screen-target",
		"mode", "app", "quiet",
		"voice", "hotkey",
		"speech", "speech-url", "speech-key", "speech-model",
		// Ключ Groq и словарь терминов: без них человеку остаётся только
		// править файлы руками, а это ровно то, от чего окно и заведено.
		"groq-key", "glossary-add", "glossary-remove",
	} {
		bindAction(window, action)
	}

	// Папку спрашивает системный диалог: в HTML её выбрать нельзя, а путь руками
	// человек вводит с ошибкой и узнаёт об этом через день.
	// История диктовки: строки читаются с диска, повтор просит служба. Окно
	// ничего не распознаёт само — иначе правила разбора разошлись бы с ней.
	_ = window.Bind("loadHistory", func() map[string]any {
		items, err := settingsapi.History(100)
		if err != nil {
			return map[string]any{"items": []settingsapi.HistoryView{}, "trouble": err.Error()}
		}
		return map[string]any{"items": items}
	})
	_ = window.Bind("retry", func(audio string) (map[string]any, error) {
		if err := settingsapi.RetryHistory(audio); err != nil {
			return nil, err
		}
		items, err := settingsapi.History(100)
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": items}, nil
	})
	_ = window.Bind("addFolder", func() (settingsapi.View, error) {
		return pickAnd("Где лежат записи разговоров", "add-folder")
	})
	_ = window.Bind("chooseRecordFolder", func() (settingsapi.View, error) {
		return pickAnd("Куда складывать записи разговоров", "record-folder")
	})

	window.SetHtml(ui.Page("settings"))
	window.Run()
}

// ── заметки ───────────────────────────────────────────────────────────────

func notesWindow() {
	window := open("Мои заметки", 780, 860)
	defer window.Destroy()

	_ = window.Bind("load", func(search string) map[string]any {
		// Улика та же, что у окна настроек: страница сообщает о себе в поток
		// ошибок. Без неё запуск окна ничего не доказывает — «белое окно» и
		// «страница не загрузилась» выглядят одинаково, а причины разные.
		fmt.Fprintln(os.Stderr, "страница заметок загрузилась")
		found, err := settingsapi.Notes(search, 200)
		if err != nil {
			// Причина едет в окно: пустая история и сломанная должны выглядеть
			// по-разному.
			return map[string]any{"items": []settingsapi.NoteView{}, "trouble": err.Error()}
		}
		return map[string]any{"items": found}
	})
	_ = window.Bind("play", settingsapi.PlayNote)
	_ = window.Bind("forget", settingsapi.ForgetNote)
	// Правка текста заметки. Значение — «номер=текст»: номер из списка, текст
	// человеческий, и знаков равенства в нём сколько угодно.
	bindAction(window, "note-text")
	_ = window.Bind("closeWindow", window.Terminate)

	window.SetHtml(ui.Page("notes"))
	window.Run()
}

// ── общее ─────────────────────────────────────────────────────────────────

// open — поднять окно.
func open(title string, width, height uint) webview.WebView {
	window := webview.NewWithOptions(webview.WebViewOptions{
		AutoFocus: true,
		WindowOptions: webview.WindowOptions{
			Title: title, Width: width, Height: height, Center: true,
		},
	})
	if window == nil {
		fmt.Fprintln(os.Stderr, "не поднялся движок окна (WebView2)")
		os.Exit(1)
	}
	return window
}

// bindAction — привязать действие ядра под тем же именем.
//
// Одним циклом, а не двадцатью обработчиками: каждый делал бы ровно одно и то
// же, а отличались бы они только опечатками.
func bindAction(window webview.WebView, action string) {
	// Приставка «set» не для красоты: в браузере уже есть window, screen и
	// document, и привязка с таким именем либо не встанет, либо затрёт своё же
	// окно. Один раз напоровшись, дальше называем все действия одинаково.
	_ = window.Bind(jsName("set-"+action), func(value string) (settingsapi.View, error) {
		if err := settingsapi.Apply(action, value); err != nil {
			return settingsapi.View{}, err
		}
		// Возвращаем новое состояние: странице не надо угадывать результат и
		// спрашивать второй раз.
		return settingsapi.Read(), nil
	})
}

// pickAnd — спросить папку и отдать её ядру.
func pickAnd(title, action string) (settingsapi.View, error) {
	chosen, err := pickFolder(title)
	if err != nil {
		return settingsapi.View{}, err
	}
	if chosen == "" {
		return settingsapi.Read(), nil // отказался — это не ошибка
	}
	if err := settingsapi.Apply(action, chosen); err != nil {
		return settingsapi.View{}, err
	}
	return settingsapi.Read(), nil
}

// jsName — имя действия так, как его зовёт страница: «screen-quality» →
// «screenQuality». Дефис в имени функции JavaScript невозможен.
func jsName(action string) string {
	parts := strings.Split(action, "-")
	for index := 1; index < len(parts); index++ {
		if parts[index] == "" {
			continue
		}
		parts[index] = strings.ToUpper(parts[index][:1]) + parts[index][1:]
	}
	return strings.Join(parts, "")
}
