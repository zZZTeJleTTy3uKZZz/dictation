// Package speech — распознавание речи у OpenAI-совместимого сервиса.
//
// Здесь два общих куска, которые до сих пор жили копиями в transcribe-go и
// Диктовке:
//
//   - клиент /audio/transcriptions (verbose_json со отрезками и временем);
//   - разбор отказа: машинный код, короткая причина и совет человеку
//     (Codes, Classify);
//   - фильтр титровых галлюцинаций Whisper (Filter, Dictionary).
//
// Чего здесь нет намеренно: клиент не читает ключ из настроек и не ходит через
// глобальный http.Client. Адрес, ключ и клиент передаёт вызывающий — он же
// решает, откуда их взять и как хранить. Поэтому пакет одинаково годится и CLI,
// и службе, и окну.
//
// Повторы (ожидание 429, backoff на 5xx) — политика вызывающего, а не клиента:
// у проверки ключа и у часовой записи она разная. Клиент делает ровно один
// запрос и возвращает разобранный отказ, по которому видно, стоит ли повторять.
package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxResponseBytes — предел ответа распознавания. Часовая запись с отрезками
// укладывается в мегабайты; больше — это уже не ответ, а недоразумение.
const maxResponseBytes = 8 << 20

// maxDetail — сколько байт тела ответа с ошибкой попадает в Error.Detail.
const maxDetail = 400

// defaultTimeout — сколько ждём ответа, если клиент не задан. Настоящая запись
// разбирается минутами, поэтому срок длинный; проверка ключа задаёт свой.
const defaultTimeout = 10 * time.Minute

// Config — настройки клиента.
type Config struct {
	// BaseURL — адрес OpenAI-совместимого API, например
	// https://api.groq.com/openai/v1. Хвост /audio/transcriptions можно не
	// писать: он дописывается.
	BaseURL string
	// Key — ключ сервиса. Пусто — запрос уходит без Authorization: у местного
	// сервиса ключа может не быть вовсе. В тексты ошибок ключ не попадает.
	Key string
	// Model — модель по умолчанию; Request.Model её перекрывает.
	Model string
	// HTTP — чем ходить. nil — свой клиент со сроком defaultTimeout. Глобальный
	// http.DefaultClient не используется: его настройки — чужие.
	HTTP *http.Client
}

// Client — клиент распознавания. Безопасен для параллельного использования.
type Client struct {
	endpoint string
	key      string
	model    string
	http     *http.Client
}

// New собирает клиент. Ошибка — адрес пуст или не разбирается.
func New(cfg Config) (*Client, error) {
	endpoint, err := TranscriptionsURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	client := cfg.HTTP
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{
		endpoint: endpoint,
		key:      strings.TrimSpace(cfg.Key),
		model:    strings.TrimSpace(cfg.Model),
		http:     client,
	}, nil
}

// TranscriptionsURL — адрес запроса из адреса API.
//
// Хвост /audio/transcriptions можно не писать (человек вставляет адрес из
// документации как есть), слэш на конце и query-строка снимаются: запрос должен
// собираться одинаково у проверки и у настоящей записи.
func TranscriptionsURL(base string) (string, error) {
	text := strings.TrimSpace(base)
	if text == "" {
		return "", errors.New("не задан адрес API распознавания")
	}
	parsed, err := url.Parse(text)
	if err != nil {
		return "", fmt.Errorf("адрес API не разбирается: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("адрес API бывает http или https, а не «%s»", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return "", errors.New("в адресе API нет имени сервера")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.RawPath = ""
	parsed.Path = strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), "/audio/transcriptions")
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String() + "/audio/transcriptions", nil
}

// Request — одна запись на распознавание.
type Request struct {
	// Audio — содержимое файла записи.
	Audio []byte
	// Filename — имя файла в форме. Пусто — audio.wav; расширение помогает
	// сервису понять формат.
	Filename string
	// Model — модель; пусто — модель из Config.
	Model string
	// Language — код языка или пусто.
	Language string
	// Prompt — подсказка распознаванию (словарь терминов, хвост предыдущего
	// текста), может быть пустой.
	Prompt string
	// Temperature — температура выборки распознавания. nil — не задана, и
	// поле в запрос не попадает: сервис берёт своё умолчание. Указатель, а не
	// float64, потому что 0 — законная температура при поиске, а не «не
	// задано».
	Temperature *float64
}

// Signals — числовые признаки отрезка, по которым видно галлюцинацию. nil
// означает, что сервис поле не прислал.
type Signals struct {
	NoSpeechProb     *float64 `json:"no_speech_prob,omitempty"`
	AvgLogprob       *float64 `json:"avg_logprob,omitempty"`
	CompressionRatio *float64 `json:"compression_ratio,omitempty"`
}

// Segment — отрезок ответа с временем внутри записи.
type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
	Signals
}

// Result — разобранный verbose_json одной записи.
type Result struct {
	Text     string    `json:"text"`
	Language string    `json:"language"`
	Model    string    `json:"model"`
	Duration float64   `json:"duration"`
	Segments []Segment `json:"segments"`
}

// Transcribe распознаёт одну запись. Контекст ограничивает запрос; клиент
// делает один HTTP-вызов, без повторов.
func (c *Client) Transcribe(ctx context.Context, req Request) (Result, error) {
	if len(req.Audio) == 0 {
		return Result{}, errors.New("нечего распознавать: звук не передан")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}
	if model == "" {
		return Result{}, errors.New("не задана модель распознавания")
	}

	body, contentType, err := multipartBody(req, model)
	if err != nil {
		return Result{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("запрос не собран: %w", err)
	}
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("Accept", "application/json")
	if c.key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.key)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, ctxErr
		}
		return Result{}, c.transportError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, ctxErr
		}
		return Result{}, &Error{Code: CodeUnavailable,
			Msg: "ответ сервиса оборвался: " + c.redact(err.Error()), Cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, c.statusError(resp.StatusCode, raw)
	}

	var out Result
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, &Error{Code: CodeBadResponse,
			Msg: "ответ сервиса не разобран: " + c.redact(err.Error())}
	}
	if out.Model == "" {
		out.Model = model
	}
	return out, nil
}

// multipartBody собирает форму с записью и полями распознавания.
func multipartBody(req Request, model string) ([]byte, string, error) {
	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)
	filename := strings.TrimSpace(req.Filename)
	if filename == "" {
		filename = "audio.wav"
	}
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		return nil, "", fmt.Errorf("поле file: %w", err)
	}
	if _, err := part.Write(req.Audio); err != nil {
		return nil, "", fmt.Errorf("запись не помещена в запрос: %w", err)
	}
	fields := [][2]string{
		{"model", model},
		{"response_format", "verbose_json"},
	}
	if language := strings.TrimSpace(req.Language); language != "" {
		fields = append(fields, [2]string{"language", language})
	}
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		fields = append(fields, [2]string{"prompt", prompt})
	}
	if req.Temperature != nil {
		fields = append(fields, [2]string{"temperature", strconv.FormatFloat(*req.Temperature, 'f', -1, 64)})
	}
	for _, field := range fields {
		if err := form.WriteField(field[0], field[1]); err != nil {
			return nil, "", fmt.Errorf("поле %s: %w", field[0], err)
		}
	}
	if err := form.Close(); err != nil {
		return nil, "", fmt.Errorf("закрытие формы: %w", err)
	}
	return body.Bytes(), form.FormDataContentType(), nil
}

// transportError — не дошли до сервиса: таймаут, TLS или обрыв.
func (c *Client) transportError(err error) *Error {
	code := codeForCause(err)
	msg := "сервис не ответил: " + c.redact(err.Error())
	switch code {
	case CodeTimeout:
		msg = "сервис не ответил вовремя: " + c.redact(err.Error())
	case CodeTLS:
		msg = "защищённое соединение не установилось: " + c.redact(err.Error())
	}
	return &Error{Code: code, Msg: msg, Cause: err}
}

// statusError разбирает ответ с ошибкой.
func (c *Client) statusError(status int, raw []byte) *Error {
	detail := truncate(c.redact(strings.TrimSpace(string(raw))), maxDetail)
	code := codeForStatus(status, raw)
	var msg string
	switch code {
	case CodeKeyRejected:
		msg = fmt.Sprintf("сервис не принял ключ (%d)", status)
	case CodeRegionBlocked:
		msg = fmt.Sprintf("сервис недоступен из этой сети (%d)", status)
		if detail != "" {
			msg += ": " + detail
		}
	case CodeRateLimited:
		msg = fmt.Sprintf("сервис ограничил запросы (%d)", status)
		if detail != "" {
			msg += ": " + detail
		}
	case CodeTimeout:
		msg = fmt.Sprintf("сервис не ответил (%d)", status)
	case CodeUnavailable:
		msg = fmt.Sprintf("сервис ответил ошибкой (%d)", status)
		if detail != "" {
			msg += ": " + detail
		}
	default:
		msg = fmt.Sprintf("сервис отказал (%d)", status)
		if detail != "" {
			msg += ": " + detail
		}
	}
	return &Error{Code: code, Status: status, Msg: msg, Detail: detail}
}

// redact убирает ключ из любого текста, который может уйти наружу.
func (c *Client) redact(s string) string {
	if c.key == "" {
		return s
	}
	return strings.ReplaceAll(s, c.key, "***")
}
