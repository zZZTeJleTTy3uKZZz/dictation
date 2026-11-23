// Настройки диктовки, которые нужны распознаванию по сети: providers.json.
//
// ЗАЧЕМ ЧУЖОЙ ФАЙЛ. Здесь были ключи распознавания, а теперь — только словарь
// терминов и запись провайдера без секрета: ключ переехал в общее хранилище
// ядра (secrets.go). Словарь живёт в том же providers.json, что и у
// причёсывания: он вёлся один на человека, и второй список терминов разошёлся
// бы с первым на первой же правке.
package speech

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/appdirs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/providercfg"
)

// providersFile — как файл зовётся у службы диктовки: имя одно на обе половины,
// и переименовать его в одну сторону нельзя.
const providersFile = "providers.json"

// DictationDir — папка настроек службы диктовки.
//
// Настройки живут вне учётной записи нарочно (см. internal/localdict/paths.go):
// уборщик, чистящий %LOCALAPPDATA%, уносил вместе с мусором ключи и словарь.
// Место ищем сами, а не спрашиваем у службы: та может быть не поднята, а ключ
// нужен значку и без неё.
func DictationDir() string {
	return appdirs.SettingsDir()
}

// ProvidersPath — где лежат настройки распознавания и причёсывания.
func ProvidersPath() string { return filepath.Join(DictationDir(), providersFile) }

// Provider — запись провайдера в providers.json, какой её видит окно диктовки.
//
// Имена полей те же, что у причёсывания, и это не совпадение: файл один. Своё
// имя поля здесь означало бы запись, которую чужая половина не прочитает.
// ProtectedKey читается, но не пишется: это прежнее место ключа, из которого
// его забирает миграция (secrets.go).
type Provider struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	BaseURL      string `json:"base_url"`
	Model        string `json:"model"`
	KeySet       bool   `json:"key_set"`
	ProtectedKey string `json:"protected_key,omitempty"`
}

// Settings — что нам нужно из providers.json: словарь терминов и провайдеры.
type Settings struct {
	Config struct {
		Glossary string `json:"glossary"`
	} `json:"config"`
	Providers []Provider `json:"providers"`
}

// ReadSettings — прочитать providers.json.
//
// ok=false — файла нет или он не разбирается. Это не ошибка и не повод отказать
// в диктовке: испорченный файл настроек не выключает работу, а распознавание без
// словаря просто работает без подсказки.
func ReadSettings(path string) (Settings, bool) {
	if path == "" {
		path = ProvidersPath()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, false
	}
	var saved Settings
	if json.Unmarshal(raw, &saved) != nil {
		return Settings{}, false
	}
	return saved, true
}

// Registered — что записано про провайдера распознавания: адрес и модель.
// Ключа здесь нет: ключ читается хранилищем (Key), а этот ответ можно показать.
type Registered struct {
	BaseURL string
	Model   string
}

// RegisteredProvider — запись провайдера распознавания по имени, а если её нет —
// по одному из узлов адреса: так запись человека, вписанная другой половиной
// программы, находится без второго такого же.
func RegisteredProvider(path, providerID string, hosts []string) (Registered, bool) {
	settings, ok := ReadSettings(path)
	if !ok {
		return Registered{}, false
	}
	var byHost *Provider
	for at, one := range settings.Providers {
		if one.ID == providerID {
			return Registered{BaseURL: one.BaseURL, Model: one.Model}, true
		}
		if byHost == nil && len(hosts) > 0 && matchesHost(one.BaseURL, hosts) {
			byHost = &settings.Providers[at]
		}
	}
	if byHost != nil {
		return Registered{BaseURL: byHost.BaseURL, Model: byHost.Model}, true
	}
	return Registered{}, false
}

// matchesHost — тот ли это сервис: сравниваются узлы адресов, а не строки.
func matchesHost(address string, hosts []string) bool {
	host := providercfg.HostOf(address)
	if host == "" {
		return false
	}
	for _, one := range hosts {
		if host == strings.ToLower(strings.TrimSpace(one)) {
			return true
		}
	}
	return false
}

// readWhole — прочитать providers.json целиком, не теряя незнакомых полей.
//
// Файла может не быть вовсе: на новой машине его ещё никто не заводил. Тогда
// начинаем с пустого — это не повод отказывать человеку в настройке. Битый файл
// по-прежнему не трогаем: в нём лежит словарь терминов, и затереть его нельзя.
func readWhole(path string) (map[string]any, error) {
	if path == "" {
		path = ProvidersPath()
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("настройки диктовки не прочитать: %w", err)
	}
	whole := map[string]any{}
	if json.Unmarshal(raw, &whole) != nil {
		return nil, fmt.Errorf("%s не разбирается — не трогаю, чините настройки диктовки", providersFile)
	}
	return whole, nil
}
