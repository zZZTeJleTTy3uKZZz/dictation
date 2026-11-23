// Мелочи: имя файла, ответ плашки, путь к себе.
package catcher

import (
	"os"
	"path/filepath"
	"strings"
)

// forbiddenInNames — символы, которые Windows не пускает в имя файла.
//
// Названия встреч приходят из заголовков окон, а там бывает что угодно:
// «Планёрка 14:00», «Отчёт 1/2», «Вопрос?». Без замены файл просто не создался
// бы, и запись пропала бы уже после разговора — хуже всего возможный момент.
const forbiddenInNames = `<>:"/\|?*`

// safeName — превратить название встречи в имя файла.
func safeName(title string) string {
	cleaned := strings.Map(func(letter rune) rune {
		if strings.ContainsRune(forbiddenInNames, letter) {
			return '-'
		}
		if letter < 32 {
			return ' '
		}
		return letter
	}, title)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	cleaned = strings.Trim(cleaned, " .-")
	if cleaned == "" {
		cleaned = "Разговор"
	}
	// Windows не принимает путь длиннее 260 символов целиком, так что на имя
	// оставляем с запасом: к нему ещё добавятся папка и расширение.
	if len(cleaned) > 120 {
		cleaned = strings.TrimSpace(cleaned[:120])
	}
	return cleaned
}

// trimLine — первая строка ответа плашки.
//
// Окно может написать в вывод и лишнее (предупреждение WebView2, перевод
// строки); решение — в первой строке, остальное нас не касается.
func trimLine(out string) string {
	first := out
	if cut := strings.IndexAny(out, "\r\n"); cut >= 0 {
		first = out[:cut]
	}
	return strings.TrimSpace(first)
}

// ownExecutable — где лежим мы сами.
//
// Рядом с нами лежит и окно: так собирается и так ставится задача планировщика,
// и искать окно в PATH было бы гаданием.
func ownExecutable() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(self)
}
