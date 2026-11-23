// Ядро настроек для внешних интерфейсов.
//
// ЗАЧЕМ ОТДЕЛЬНАЯ КОМАНДА. Окно может быть любым — на WebView2, на Flutter, да
// хоть строкой в консоли, — но правила настроек должны остаться одни. Интерфейс
// спрашивает состояние и присылает просьбы, а что из этого законно, решает Go.
//
// Канал нарочно простейший: запуск процесса, JSON в стандартный вывод, код
// возврата вместо протокола ошибок. Ни портов, ни служб, ни токенов — нечему
// сломаться и нечего настраивать.
//
//	meetdigest-core show                → состояние настроек одним JSON
//	meetdigest-core set mode whitelist  → применить просьбу и вывести новое состояние
//	meetdigest-core folder "Где записи" → спросить папку системным диалогом
//	meetdigest-core notes [слово]       → история заметок (поиск по слову)
//	meetdigest-core note <id>           → одна заметка целиком
//	meetdigest-core note-play <id>      → послушать заметку
//	meetdigest-core note-forget <id>    → убрать заметку со звуком
//	meetdigest-core dictate             → диктовка: текст печатается в активное окно
//	meetdigest-core doctor              → проверить диктовку и починить, что можно
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/localdict"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/settingsapi"
)

// coreVersion — что умеет эта сборка.
//
// Не номер, а список умений: установщик проверяет им не «новее ли», а «есть ли
// то, ради чего собирали». Номер пришлось бы помнить и поднимать руками.
const coreVersion = "show set folder dictate doctor notes note note-play note-forget"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "нужна команда: show, set, folder, dictate, doctor, "+
			"notes, note, note-play, note-forget")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "version":
		// Нужна установщику: по ней он убеждается, что на диске оказалась
		// свежая программа, а не та, которую держал запущенный процесс.
		fmt.Println(coreVersion)
	case "show":
		write(settingsapi.Read())
	case "set":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "нужно действие: set <действие> [значение]")
			os.Exit(2)
		}
		value := ""
		if len(os.Args) > 3 {
			value = os.Args[3]
		}
		if err := settingsapi.Apply(os.Args[2], value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// Отдаём новое состояние сразу: интерфейсу не нужно ни угадывать
		// результат, ни спрашивать второй раз — показать он может только то,
		// что действительно записалось.
		write(settingsapi.Read())
	case "folder":
		chosen, err := pickFolder(folderTitle())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// Пустая строка означает «отказался»: это не ошибка.
		fmt.Println(chosen)
	case "dictate":
		os.Exit(dictate())
	case "doctor":
		// Тот же лекарь, что и у службы: диктовка — это четыре программы, и
		// спрашивать о ней надо с любой стороны одинаково (см. localdict.Doctor).
		// Отчёт — и на экран, и в файл: ядро зовут и из окна, где консоли нет.
		localdict.AttachConsole()
		checks := localdict.Doctor(context.Background())
		var report strings.Builder
		code := localdict.PrintDoctor(checks, &report)
		fmt.Print(report.String())
		localdict.WriteDoctorReport(report.String())
		os.Exit(code)
	case "notes":
		search := ""
		if len(os.Args) > 2 {
			search = os.Args[2]
		}
		found, err := settingsapi.Notes(search, 200)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		write(map[string]any{"items": found})
	case "note":
		note, err := settingsapi.Note(need("нужен номер заметки"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		write(note)
	case "note-play":
		if err := settingsapi.PlayNote(need("нужен номер заметки")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "note-forget":
		if err := settingsapi.ForgetNote(need("нужен номер заметки")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// Отдаём новую историю: окну не нужно спрашивать ещё раз, чтобы убрать
		// строку, и оно не покажет удалённое.
		found, err := settingsapi.Notes("", 200)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		write(map[string]any{"items": found})
	default:
		fmt.Fprintln(os.Stderr, "непонятная команда:", os.Args[1])
		os.Exit(2)
	}
}

// need — обязательный аргумент команды.
func need(why string) string {
	if len(os.Args) > 2 && strings.TrimSpace(os.Args[2]) != "" {
		return os.Args[2]
	}
	fmt.Fprintln(os.Stderr, why)
	os.Exit(2)
	return ""
}

func folderTitle() string {
	if len(os.Args) > 2 {
		return os.Args[2]
	}
	return "Выберите папку"
}

func write(value any) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "состояние не сложилось:", err)
		os.Exit(1)
	}
	fmt.Println(string(raw))
}
