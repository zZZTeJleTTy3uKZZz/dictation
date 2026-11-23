// Package conn — подключение к провайдеру распознавания речи.
//
// Здесь всё, что нужно, чтобы провайдера настроить, перенести и проверить:
//
//   - Provider — запись настроек без ключа (ключ живёт в SecretStore);
//   - ссылка speech-provider://v1/… — настройка одной строкой, чтобы её можно
//     было прислать в мессенджере; старая ссылка Диктовки dictation://… тоже
//     читается, но выпускается только новая;
//   - Check — настоящий пробный запрос через клиент speech;
//   - прокси — строкой настройки в транспорт (разбор даёт mybits/egress);
//   - SecretStore — где лежит ключ: файл 0600 или DPAPI на Windows.
//
// Ключ намеренно не поле Provider: запись настроек можно показать, положить в
// журнал и передать в UI, и в ней не окажется секрета.
package conn

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/egress"
)

// Kind — вид провайдера распознавания.
type Kind string

const (
	// KindGroq — облако Groq (OpenAI-совместимый /audio/transcriptions).
	KindGroq Kind = "groq"
	// KindCustom — свой OpenAI-совместимый сервис.
	KindCustom Kind = "custom"
	// KindSpeechID — провайдер, доступ к которому даёт вход через Цифро ID.
	KindSpeechID Kind = "speech_id"
)

// GroqBaseURL и GroqModel — умолчания Groq: их подставляет Normalize, когда
// человек выбрал Groq и не вписал адрес с моделью.
const (
	GroqBaseURL = "https://api.groq.com/openai/v1"
	GroqModel   = "whisper-large-v3"
)

// Provider — запись провайдера в настройках. Ключа здесь нет.
type Provider struct {
	Kind    Kind   `json:"kind"`
	Name    string `json:"name,omitempty"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	// Proxy — выход наружу строкой настройки: socks5://узел:порт,
	// http://узел:порт. Пусто — запрос идёт прямо. Разбирает его mybits/egress,
	// своего разбора здесь нет.
	Proxy string `json:"proxy,omitempty"`
}

// Valid сообщает, известен ли вид провайдера.
func (k Kind) Valid() bool {
	switch k {
	case KindGroq, KindCustom, KindSpeechID:
		return true
	default:
		return false
	}
}

// KindOf — вид провайдера по адресу. Groq узнаётся по узлу, всё остальное —
// свой сервис. Нужно разбору ссылки: вид в ссылку не пишется, он следует из
// адреса.
func KindOf(baseURL string) Kind {
	if HostOf(baseURL) == "api.groq.com" {
		return KindGroq
	}
	return KindCustom
}

// Normalize проверяет запись и приводит её к одному виду: подставляет умолчания
// известного провайдера, нормализует адрес, называет провайдера узлом, если имя
// не задано, и проверяет строку выхода.
func (p Provider) Normalize() (Provider, error) {
	if p.Kind == "" {
		p.Kind = KindOf(p.BaseURL)
	}
	if !p.Kind.Valid() {
		return Provider{}, fmt.Errorf("неизвестный вид провайдера «%s»: знаем groq, custom, speech_id", p.Kind)
	}
	if p.Kind == KindGroq {
		if strings.TrimSpace(p.BaseURL) == "" {
			p.BaseURL = GroqBaseURL
		}
		if strings.TrimSpace(p.Model) == "" {
			p.Model = GroqModel
		}
	}
	base, err := NormalizeBaseURL(p.BaseURL)
	if err != nil {
		return Provider{}, err
	}
	p.BaseURL = base
	p.Model = strings.TrimSpace(p.Model)
	if p.Model == "" {
		return Provider{}, errors.New("не задана модель распознавания")
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		p.Name = HostOf(p.BaseURL)
	}
	p.Proxy = strings.TrimSpace(p.Proxy)
	if p.Proxy != "" {
		// Разбор тот же, каким транспорт будет соединяться: «сохранилось»
		// должно значить «поднимется».
		if _, err := egress.Parse(p.Proxy); err != nil {
			return Provider{}, err
		}
	}
	return p, nil
}

// lengths — пределы полей: ссылку присылают в мессенджере, и километровая
// строка там не живёт. Значения взяты с большим запасом к настоящим.
const (
	maxName    = 100
	maxBaseURL = 500
	maxModel   = 100
	maxKey     = 4096
)

// NormalizeBaseURL проверяет адрес API и убирает хвост расшифровки.
//
// Человек вставляет адрес из чужой документации — с хвостом
// /audio/transcriptions, со слэшем на конце, иногда с query. Приводим к одному
// виду здесь, чтобы запрос собирался одинаково и у проверки, и у диктовки.
func NormalizeBaseURL(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", errors.New("не задан адрес API")
	}
	if !strings.Contains(text, "://") {
		// Адрес без схемы — обычная запись в настройках; для проверки это
		// подсказка человеку, а не повод угадывать, https там или http.
		return "", errors.New("адрес API должен быть полным: https://сервер/v1")
	}
	if len(text) > maxBaseURL {
		return "", errors.New("адрес API слишком длинный")
	}
	parsed, err := url.Parse(text)
	if err != nil {
		return "", fmt.Errorf("адрес API не разбирается: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "https":
	case "http":
		if !isLocalHost(parsed.Hostname()) {
			return "", errors.New("адрес API по http допустим только для этой же машины (localhost): ключ по открытому каналу в сеть не уходит")
		}
	default:
		return "", fmt.Errorf("адрес API бывает только https (или http для localhost), а не «%s»", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return "", errors.New("в адресе API нет имени сервера")
	}
	if parsed.User != nil {
		return "", errors.New("имя и пароль в адресе API не пишут — они передаются отдельными полями")
	}
	if parsed.Fragment != "" {
		return "", errors.New("у адреса API не бывает хвоста после #")
	}
	parsed.Scheme = scheme
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.RawPath = ""
	parsed.Path = strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), "/audio/transcriptions")
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

// isLocalHost — та ли это машина. Только для неё http не страшен: звук и ключ
// не выходят за пределы компьютера.
func isLocalHost(host string) bool {
	name := strings.ToLower(strings.TrimSpace(host))
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return true
	}
	if ip := net.ParseIP(name); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// HostOf — узел адреса: по нему называется провайдер, если имени не дали.
func HostOf(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	if !strings.Contains(text, "://") {
		text = "https://" + text
	}
	parsed, err := url.Parse(text)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}
