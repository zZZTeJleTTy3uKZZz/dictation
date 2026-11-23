// Настройка провайдера из файла — для тихой установки и обновления.
//
// ЗАЧЕМ. Наши устройства ставятся без человека у экрана: установщик получает
// /CONFIG="путь" и должен оставить провайдера уже настроенным, иначе первый же
// запуск покажет онбординг и диктовка не заработает до чьей-то руки. Ссылка-
// конфигурация — тот же формат, что человек вставляет в окне, и
// разбирает его ровно тот же код (internal/providercfg): второго разбора быть
// не должно, иначе две половины поймут одну ссылку по-разному.
//
// ЧЕГО ЗДЕСЬ НЕТ. Ключ не печатается ни в ответе, ни в ошибках: файл читает
// установщик, а ключ — секрет, которому в журнале установки не место.
package dictconfig

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/providercfg"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
)

// maxLinkFile — предел размера файла. Ссылка с ключом — сотни байт; мегабайт
// здесь значит, что вместо конфигурации положили что-то другое.
const maxLinkFile = 64 * 1024

// Applied — что настроено, без ключа. По этому ответу установщик и человек
// видят, что провайдер принят, а секрет остаётся в файле настроек.
type Applied struct {
	Name   string
	URL    string
	Model  string
	KeySet bool
}

// ReadLinkFile читает файл со ссылкой-конфигурацией: одна строка, необязательный
// BOM и хвостовые переводы строк. Пустой файл и несколько строк — ошибка, а не
// «возьмём первую»: молча выбранная не та строка настроила бы не того
// провайдера.
func ReadLinkFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("не назван файл конфигурации")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("файл конфигурации не читается: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("файл конфигурации — это каталог: %s", path)
	}
	if info.Size() > maxLinkFile {
		return "", fmt.Errorf("файл конфигурации больше %d КБ: это не ссылка", maxLinkFile/1024)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("файл конфигурации не читается: %w", err)
	}
	text := strings.TrimPrefix(string(raw), "\ufeff")
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return "", errors.New("файл конфигурации пуст")
	}
	lines := strings.Split(text, "\n")
	nonEmpty := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, strings.TrimSpace(line))
		}
	}
	if len(nonEmpty) != 1 {
		return "", fmt.Errorf("в файле конфигурации %d непустых строк: нужна ровно одна ссылка", len(nonEmpty))
	}
	return nonEmpty[0], nil
}

// ApplyLinkText разбирает ссылку и записывает провайдера в настройки диктовки.
//
// Записываются ровно те поля, что и при вставке ссылки в окне: способ
// «свой сервис», адрес, модель и ключ. Ключ ставится и пустым: ссылка без ключа
// значит «ключа нет», а не «оставь прежний».
func ApplyLinkText(raw string) (Applied, error) {
	link, err := providercfg.Parse(raw)
	if err != nil {
		// Ошибки разбора человеку понятны и без самой ссылки: ключ в них не
		// возвращаем даже случайно.
		return Applied{}, fmt.Errorf("ссылка-конфигурация не принята: %w", err)
	}
	voice := prefs.LoadVoice("")
	if voice == nil {
		return Applied{}, errors.New("настройки диктовки не прочитались")
	}
	// Ключ из ссылки — в хранилище ключей, а не открытым текстом в voice.json:
	// приложение, которое получают установленные машины, может уехать вместе с
	// домом человека. Имя ключа строится из адреса, к которому он принадлежит;
	// сверка — до записи настроек: не сошлось — провайдер остаётся прежним.
	if key := strings.TrimSpace(link.Key); key != "" {
		if _, err := speech.SetKeyChecked(speech.ProvidersPath(), link.URL, key); err != nil {
			// В ошибке сверки значения ключа нет — она его не печатает.
			return Applied{}, fmt.Errorf("ключ из ссылки не сохранён: %w", err)
		}
	}
	voice.Speech = "api"
	voice.SpeechURL = link.URL
	voice.SpeechModel = link.Model
	// Пусто всегда, а не «оставь как было»: ссылка без ключа значит «ключа
	// нет», а прежний открытый ключ — лишь источник переноса в хранилище
	// (speech/secrets.go), и он туда уже переехал или переедет на чтении.
	voice.SpeechKey = ""
	if err := voice.Save(); err != nil {
		return Applied{}, fmt.Errorf("настройки не сохранены: %w", err)
	}
	return Applied{
		Name:   link.Name,
		URL:    link.URL,
		Model:  link.Model,
		KeySet: strings.TrimSpace(link.Key) != "",
	}, nil
}

// ApplyLinkFile — прочитать файл и применить ссылку. Файл не удаляется: он
// нужен и человеку, и повторной выкатке.
func ApplyLinkFile(path string) (Applied, error) {
	raw, err := ReadLinkFile(path)
	if err != nil {
		return Applied{}, err
	}
	return ApplyLinkText(raw)
}
