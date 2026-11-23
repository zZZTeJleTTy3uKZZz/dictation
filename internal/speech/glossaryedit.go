package speech

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Словарь терминов лежит в providers.json рядом с ключами провайдеров и настройками
// причёсывания, и правится он здесь, а не в каждом окне по-своему: два места для
// одного словаря разошлись бы так же, как разошлись когда-то правила по Teams.

// AddGlossaryTerm — вписать термин в словарь. Второй ответ — был ли он там уже:
// вписывать повторно нечего, и делать вид, что словарь изменился, не надо.
//
// Термин встаёт в начало словаря нарочно. Подсказка для распознавания обрезается
// по пределу длины, и всё, что стоит в конце, до распознавания не доходит: словарь
// уже подошёл к пределу вплотную, и дописанный в конец термин не работал бы вовсе.
func AddGlossaryTerm(path, term string) (bool, error) {
	return editGlossary(path, func(terms []string) ([]string, bool, error) {
		term = strings.TrimSpace(term)
		if term == "" {
			return nil, false, errors.New("нужен термин")
		}
		for _, one := range terms {
			if strings.EqualFold(strings.TrimSpace(one), term) {
				return terms, false, nil
			}
		}
		return append([]string{term}, terms...), true, nil
	})
}

// RemoveGlossaryTerm — убрать термин из словаря. Так откатывают вписанное зря:
// распознаватель слышит слово неверно, и закреплять эту ошибку нельзя.
func RemoveGlossaryTerm(path, term string) (bool, error) {
	return editGlossary(path, func(terms []string) ([]string, bool, error) {
		term = strings.TrimSpace(term)
		if term == "" {
			return nil, false, errors.New("нужен термин")
		}
		kept := make([]string, 0, len(terms))
		removed := false
		for _, one := range terms {
			if strings.EqualFold(strings.TrimSpace(one), term) {
				removed = true
				continue
			}
			kept = append(kept, one)
		}
		return kept, removed, nil
	})
}

// editGlossary — прочитать словарь, отдать его на правку и записать обратно.
//
// Файл читается и правится как есть, а не пересобирается из знакомых полей: рядом
// лежат ключи провайдеров и настройки причёсывания, и потерять их при правке
// словаря нельзя. Запись идёт через временный файл: половина настроек хуже их
// отсутствия, а служба диктовки читает этот файл на ходу.
func editGlossary(path string, change func([]string) ([]string, bool, error)) (bool, error) {
	if path == "" {
		path = ProvidersPath()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("настроек диктовки нет (%s): словарь править негде", providersFile)
	}
	whole := map[string]any{}
	if json.Unmarshal(raw, &whole) != nil {
		return false, fmt.Errorf("%s не разбирается — словарь не трогаю, чините настройки диктовки", providersFile)
	}
	config, ok := whole["config"].(map[string]any)
	if !ok {
		config = map[string]any{}
	}
	updated, changed, err := change(splitGlossary(config["glossary"]))
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	config["glossary"] = strings.Join(updated, ", ")
	whole["config"] = config

	written, err := json.MarshalIndent(whole, "", "  ")
	if err != nil {
		return false, err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(written, '\n'), 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(temporary, path); err != nil {
		return false, err
	}
	return true, nil
}

// splitGlossary — разобрать словарь, записанный через запятую.
func splitGlossary(value any) []string {
	text, _ := value.(string)
	terms := []string{}
	for _, one := range strings.Split(text, ",") {
		if trimmed := strings.TrimSpace(one); trimmed != "" {
			terms = append(terms, trimmed)
		}
	}
	return terms
}
