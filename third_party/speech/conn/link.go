package conn

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Схемы ссылки.
//
// Новая ссылка — обычный адрес со схемой speech-provider и хозяином-версией v1:
// человек видит, что вставляет, а версия позволяет однажды поменять нагрузку,
// не угадывая формат по содержимому. Старая схема dictation остаётся
// читаемой: ссылки уже разосланы.
const (
	LinkScheme   = "speech-provider"
	LinkVersion  = "v1"
	LegacyScheme = "dictation"
	LegacyHost   = "provider"
)

// apiOpenAICompatible — единственный API, который умеет speech.
const apiOpenAICompatible = "openai-compatible"

// linkPayload — нагрузка ссылки. Поля и их смысл — в
// contracts/schemas/speech-provider/link.json.
type linkPayload struct {
	Kind    string `json:"kind"`
	API     string `json:"api"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	Key     string `json:"key,omitempty"`
	Proxy   string `json:"proxy,omitempty"`
	Name    string `json:"name,omitempty"`
}

// Link — провайдер и ключ, каким их переносит ссылка. Ключ может быть пустым:
// по умолчанию ссылкой делятся без секрета.
type Link struct {
	Provider Provider
	Key      string
}

// Parse разбирает ссылку-конфигурацию: новую speech-provider://v1/… и старую
// dictation://provider?…
//
// Ошибки названы так, чтобы человеку было что исправить: «это не ссылка
// провайдера», «нагрузка не декодируется», «в ссылке нет модели». Молчаливое
// «неверная ссылка» заставляло бы гадать, какая именно часть не понравилась.
func Parse(raw string) (Link, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return Link{}, errors.New("ссылка пуста")
	}
	if utf8.RuneCountInString(text) > maxName+maxBaseURL+maxModel+maxKey+2048 {
		return Link{}, errors.New("ссылка слишком длинная — это не конфигурация провайдера")
	}
	lower := strings.ToLower(text)
	switch {
	case strings.HasPrefix(lower, LinkScheme+"://"):
		return parseVersioned(text)
	case strings.HasPrefix(lower, LegacyScheme+"://"):
		return parseLegacy(text)
	default:
		return Link{}, fmt.Errorf("это не ссылка провайдера: нужна %s://%s/… или старая %s://%s?…",
			LinkScheme, LinkVersion, LegacyScheme, LegacyHost)
	}
}

// parseVersioned разбирает новую ссылку.
func parseVersioned(text string) (Link, error) {
	parsed, err := url.Parse(text)
	if err != nil {
		return Link{}, fmt.Errorf("ссылка не разбирается: %w", err)
	}
	if parsed.User != nil {
		return Link{}, errors.New("в ссылке-конфигурации не бывает имени и пароля — ключ передаётся полем key")
	}
	if !strings.EqualFold(parsed.Host, LinkVersion) {
		return Link{}, fmt.Errorf("в ссылке провайдера ожидается версия %s, а не «%s»", LinkVersion, parsed.Host)
	}
	if parsed.RawQuery != "" {
		return Link{}, errors.New("у ссылки-конфигурации не бывает query: всё лежит в нагрузке после слэша")
	}
	if parsed.Fragment != "" {
		return Link{}, errors.New("у ссылки-конфигурации не бывает хвоста после #")
	}
	encoded := strings.TrimPrefix(parsed.Path, "/")
	if encoded == "" {
		return Link{}, errors.New("в ссылке нет нагрузки: после /v1/ идёт base64url с JSON")
	}
	if strings.Contains(encoded, "/") {
		return Link{}, errors.New("нагрузка ссылки — один кусок base64url, без слэшей")
	}
	payload, err := decodePayload(encoded)
	if err != nil {
		return Link{}, err
	}
	var wire linkPayload
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Link{}, fmt.Errorf("нагрузка ссылки не разбирается: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Link{}, errors.New("в нагрузке ссылки лишние данные после JSON")
	}
	if wire.Kind != "speech" {
		return Link{}, fmt.Errorf("в ссылке ожидается kind=speech, а не «%s»", wire.Kind)
	}
	if wire.API != apiOpenAICompatible {
		return Link{}, fmt.Errorf("в ссылке ожидается api=%s, а не «%s»", apiOpenAICompatible, wire.API)
	}
	if len(wire.Key) > maxKey {
		return Link{}, errors.New("ключ в ссылке слишком длинный")
	}
	provider, err := Provider{
		Kind:    KindOf(wire.BaseURL),
		Name:    wire.Name,
		BaseURL: wire.BaseURL,
		Model:   wire.Model,
		Proxy:   wire.Proxy,
	}.Normalize()
	if err != nil {
		return Link{}, err
	}
	return Link{Provider: provider, Key: strings.TrimSpace(wire.Key)}, nil
}

// parseLegacy разбирает старую ссылку Диктовки.
//
//	dictation://provider?name=Groq&url=https%3A%2F%2Fapi.groq.com%2Fopenai%2Fv1&model=whisper-large-v3&key=ключ…
//
// Формат перенесён без изменений: по нему уже настроены чужие машины.
func parseLegacy(text string) (Link, error) {
	parsed, err := url.Parse(text)
	if err != nil {
		return Link{}, fmt.Errorf("ссылка не разбирается: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, LegacyScheme) {
		return Link{}, fmt.Errorf("это не ссылка Диктовки: нужна %s://%s?…", LegacyScheme, LegacyHost)
	}
	if !strings.EqualFold(parsed.Host, LegacyHost) {
		return Link{}, fmt.Errorf("в ссылке Диктовки ожидается %s://%s, а не «%s»", LegacyScheme, LegacyHost, parsed.Host)
	}
	if parsed.User != nil {
		return Link{}, errors.New("в ссылке-конфигурации не бывает имени и пароля — ключ передаётся полем key")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return Link{}, errors.New("у ссылки-конфигурации не бывает пути после provider")
	}
	if parsed.Fragment != "" {
		return Link{}, errors.New("у ссылки-конфигурации не бывает хвоста после #")
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return Link{}, fmt.Errorf("поля ссылки не разбираются: %w", err)
	}
	for _, name := range []string{"name", "url", "model", "key"} {
		if len(values[name]) > 1 {
			return Link{}, fmt.Errorf("поле %s в ссылке повторено — непонятно, какое из них верное", name)
		}
	}
	found := Link{
		Provider: Provider{
			Kind:    KindOf(values.Get("url")),
			Name:    strings.TrimSpace(values.Get("name")),
			BaseURL: strings.TrimSpace(values.Get("url")),
			Model:   strings.TrimSpace(values.Get("model")),
		},
		Key: strings.TrimSpace(values.Get("key")),
	}
	if utf8.RuneCountInString(found.Provider.Name) > maxName {
		return Link{}, errors.New("название провайдера в ссылке слишком длинное")
	}
	if utf8.RuneCountInString(found.Provider.Model) > maxModel {
		return Link{}, errors.New("модель в ссылке слишком длинная")
	}
	if len(found.Key) > maxKey {
		return Link{}, errors.New("ключ в ссылке слишком длинный")
	}
	normalized, err := found.Provider.Normalize()
	if err != nil {
		return Link{}, err
	}
	found.Provider = normalized
	return found, nil
}

// Encode собирает новую ссылку-конфигурацию. Старый формат не выпускается
// никогда: одна ссылка на все случаи, и это новая.
func Encode(link Link) (string, error) {
	provider, err := link.Provider.Normalize()
	if err != nil {
		return "", err
	}
	if len(link.Key) > maxKey {
		return "", errors.New("ключ слишком длинный для ссылки")
	}
	wire := linkPayload{
		Kind:    "speech",
		API:     apiOpenAICompatible,
		BaseURL: provider.BaseURL,
		Model:   provider.Model,
		Key:     strings.TrimSpace(link.Key),
		Proxy:   provider.Proxy,
		Name:    provider.Name,
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return "", fmt.Errorf("нагрузка ссылки не собрана: %w", err)
	}
	return LinkScheme + "://" + LinkVersion + "/" + base64.RawURLEncoding.EncodeToString(payload), nil
}

// Share собирает ссылку для передачи другому человеку.
//
// withKey=false (умолчание) — ссылка без ключа: её можно показать, переслать и
// положить в журнал. С ключом — осознанный обмен секретом, и тогда строка сама
// становится секретом.
func Share(provider Provider, key string, withKey bool) (string, error) {
	link := Link{Provider: provider}
	if withKey {
		link.Key = key
	}
	return Encode(link)
}

// decodePayload декодирует base64url; выравнивание по вкусу — с ним и без него.
func decodePayload(encoded string) ([]byte, error) {
	if payload, err := base64.RawURLEncoding.DecodeString(encoded); err == nil {
		return payload, nil
	}
	if payload, err := base64.URLEncoding.DecodeString(encoded); err == nil {
		return payload, nil
	}
	return nil, errors.New("нагрузка ссылки не декодируется: нужен base64url с JSON")
}
