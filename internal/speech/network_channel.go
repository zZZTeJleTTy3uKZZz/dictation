// Способы по сети: свою запись отправляем клиентом ядра speech.
//
// Клиент ядра (`speech.New`/`Client.Transcribe`) делает один запрос и разбирает
// отказ в speech.Error с кодом — сеть, таймаут, TLS, предел частоты, 5xx,
// разбор ответа. Повторы (ожидание предела по Retry-After, краткий сбой службы),
// классификация отказа и всё, что нужно вызывающему, живут здесь, чтобы один и
// тот же разбор был и у строки истории, и у плашки выключения.
package speech

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	core "github.com/zZZTeJleTTy3uKZZz/dictation/third_party/speech"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/dictsegment"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/providercfg"
)

// Remote — куда и чем стучаться.
//
// Совместимо с тем, что умеют почти все: отправка файла формой на адрес вида
// …/audio/transcriptions, ответ с полем text и отрезками (verbose_json). Так
// устроены и Groq, и OpenAI, и местные сервисы, которые им подражают, — значит
// один способ закрывает их все, и на каждый новый не нужно писать свой.
type Remote struct {
	URL   string
	Key   string
	Model string
	// Language — подсказка распознавателю. Для русского заметно повышает
	// качество: без неё короткую фразу принимают за английскую.
	Language string
	// Prompt — подсказка о том, как писать. Едет тем же запросом, что и звук, и
	// без неё whisper пишет имена строчными и вкривь («лидеруб» вместо
	// «Лидоруб»), а знаков препинания не ставит вовсе. Проверено замерами.
	Prompt string
	// Wait — срок на весь разбор вместе с ожиданием предела.
	Wait time.Duration
	// HTTP — чем ходить в сервис. Пусто — обычный клиент. Нужен ради прокси:
	// из некоторых сетей прямой запрос не доходит вовсе, и тогда человек
	// вписывает посредника, через которого диктовка и ходит.
	HTTP *http.Client
	// OnWait — кому сказать, что мы ждём лимита. Сервис отвечает 429 и называет
	// срок (Retry-After); ждать молча нельзя: человек уже отпустил клавишу и
	// смотрит на пустое поле, и «ничего не происходит» он прочитает как поломку.
	// Зовётся перед каждой задержкой: сколько ждать и какая это попытка.
	OnWait func(wait time.Duration, attempt int)
}

// OpenAICompatible — сервис по сети: чужой провайдер, вписанный человеком.
type OpenAICompatible struct{ Remote }

// NewOpenAICompatible — способ «сервис по сети».
func NewOpenAICompatible(where Remote) *OpenAICompatible {
	if where.Wait <= 0 {
		where.Wait = 3 * time.Minute
	}
	if where.Language == "" {
		where.Language = "ru"
	}
	return &OpenAICompatible{Remote: where}
}

func (o *OpenAICompatible) Name() string  { return "api" }
func (o *OpenAICompatible) Title() string { return "Сервис по сети" }

func (o *OpenAICompatible) Ready() (bool, string) {
	if o.URL == "" {
		return false, "не задан адрес сервиса"
	}
	return readyFor(o.URL, o.Key)
}

// readyFor — можно ли идти в сервис по адресу с этим ключом.
//
// Ключ нашего роутера чужому сервису не годится: там свой ключ, и с нашим
// запрос уйдёт и получит отказ, а в журнале останется «неверный ключ» у нашего
// роутера. Поэтому смена адреса без своего ключа — это «нечем ходить», а не
// «попробуем». Место ключа узнаётся по узлу адреса (providercfg).
func readyFor(address, key string) (bool, string) {
	name := providerSecretName(address)
	if name == "" {
		return false, "в адресе сервиса нет узла — некуда идти"
	}
	if key == "" {
		if host := providercfg.HostOf(address); host != "" {
			return false, "у сервиса " + host + " нет своего ключа: впишите его в настройках"
		}
		return false, "не заданы адрес и ключ сервиса"
	}
	return true, ""
}

func (o *OpenAICompatible) Text(ctx context.Context, path string) (string, error) {
	return o.TextWithPrompt(ctx, path, "")
}

// TextWithPrompt — разбор с хвостом предыдущего текста в подсказке.
//
// Хвост добавляется к подсказке, а не заменяет её: человек мог вписать свою
// (Prompt), и она остаётся. Зовут этот путь только те, кто знает, что способ
// поле prompt принимает, — выбор делает localdict/channelText.
func (o *OpenAICompatible) TextWithPrompt(ctx context.Context, path, tail string) (string, error) {
	if ready, why := o.Ready(); !ready {
		return "", errors.New(why)
	}
	prompt := o.Prompt
	if strings.TrimSpace(tail) != "" {
		prompt = PromptWithContext(prompt, tail)
	}
	return transcribeFile(ctx, transcribeOptions{
		URL: o.URL, Key: o.Key, Model: o.Model, Language: o.Language,
		Prompt: prompt, Wait: o.Wait, HTTP: o.HTTP, OnWait: o.OnWait,
	}, path)
}

// PromptWithContext — словарь терминов и хвост предыдущего текста в одном поле
// prompt. Сам разбор — в dictsegment: там же он и проверяется.
func PromptWithContext(hint, tail string) string {
	return dictsegment.PromptWithContext(hint, tail, hintLimit)
}

// Сколько раз пробуем, когда сервис отвечает «занят лимитом», и сколько ждём,
// если срок он не назвал. Число попыток конечно нарочно: сервис, который просит
// подождать и в третий раз, всё равно не ответит, а человек всё это время ждёт.
const (
	rateLimitAttempts = 3
	rateLimitWait     = 5 * time.Second
)

// RecognitionTemperature — температура распознавания: ноль.
//
// Ноль, а не «как повезёт»: замеры на одной и той же записи
// дали при temperature=0 три прогона со текстом, совпавшим знак в знак, а без
// поля ответ не воспроизводим. Заметка человека — не творчество: одно и то же
// сказанное на одной и той же записи должно читаться одинаково.
//
// Указатель нарочно: ядро различает «не задано» (nil — поле не уходит) и «ноль»
// (поле уходит). Поле, а не константа в запросе, — чтобы однажды его можно было
// поставить в настройки, не переписывая путь запроса.
var RecognitionTemperature = func() *float64 {
	zero := 0.0
	return &zero
}()

// transcribeOptions — что нужно любому сетевому способу, чтобы разобрать запись.
type transcribeOptions struct {
	URL, Key, Model, Language, Prompt string
	Wait                              time.Duration
	HTTP                              *http.Client
	OnWait                            func(wait time.Duration, attempt int)
}

// transcribeFile — разобрать файл записи клиентом ядра с повторами.
//
// Отказ ядра (speech.Error с кодом) проходит как есть, дополненный сроком из
// Retry-After: по нему решает вызывающий — повторять строку истории или нет, —
// и он же его объясняет человеку.
func transcribeFile(ctx context.Context, options transcribeOptions, path string) (string, error) {
	audio, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("открыть запись: %w", err)
	}
	client, err := core.New(core.Config{
		BaseURL: options.URL,
		Key:     options.Key,
		Model:   options.Model,
		// Заголовки ответа ядро не отдаёт, а срок ожидания предела сервис
		// называет в Retry-After — их запоминает транспорт под id попытки.
		HTTP: HeaderClient(options.HTTP),
	})
	if err != nil {
		return "", err
	}
	ask := core.Request{
		Audio:    audio,
		Filename: filepath.Base(path),
		Model:    options.Model,
		Language: options.Language,
		Prompt:   options.Prompt,
		// Ноль — ради воспроизводимости (см. RecognitionTemperature).
		Temperature: RecognitionTemperature,
	}

	wait := options.Wait
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	deadline, _ := deadlineCtx.Deadline()

	// Начало разбора: срок окончательного отказа — «сколько осталось» от него.
	failed := time.Now()

	// Лимит — не отказ, а срок: сервис говорит, когда приходить снова. Ждём и
	// повторяем, пока хватает времени у самого запроса. Запрос собирается заново
	// на каждую попытку: тело у нас в памяти, и оно не портится от отправки.
	for attempt := 1; ; attempt++ {
		id := nextRequestID()
		result, err := client.Transcribe(withRequestID(deadlineCtx, id), ask)
		if err == nil {
			return resultText(result), nil
		}
		if ctx.Err() != nil {
			// Отменили (человек закрыл окно, служба гасится) — не повторяем.
			return "", ctx.Err()
		}
		headers := takeHeaders(id)
		// Повторяем здесь только предел частоты: сам по себе он пройдёт за
		// секунды, и ждать его в поле, куда человек только что продиктовал,
		// лучше, чем заставлять его нажимать «ещё раз». Прочие транзиентные
		// отказы (5xx, таймаут, срыв сети) — дело строки истории со своим
		// расписанием (localdict/retry.go), и здесь мы их не ждём.
		if classifyCode(err) != core.CodeRateLimited {
			return "", attachRetryAfter(err, headers)
		}
		if attempt >= rateLimitAttempts {
			return "", failedRefusal(err, failed, "попыток переждать предел больше нет")
		}
		serviceWait := retryAfter(headers.Get("Retry-After"), rateLimitWait)
		// Срок у сервиса бывает длиннее нашего запаса, и ждать его целиком
		// здесь незачем: выше есть повторы с паузой, а человек не должен
		// стоять минуту перед пустым полем.
		wait := min(serviceWait, rateLimitWait)
		left := time.Until(deadline)
		if wait >= left {
			return "", failedRefusal(err, failed, fmt.Sprintf(
				"сервис просит подождать %s, а разбору осталось %s",
				serviceWait.Round(time.Second), left.Round(time.Second)))
		}
		if options.OnWait != nil {
			options.OnWait(wait, attempt)
		}
		if !sleepWithContext(deadlineCtx, wait) {
			return "", failedRefusal(err, failed, "ждать предела нечем: разбор прерван")
		}
	}
}

// classifyCode — код отказа ядра по ошибке клиента.
//
// У ошибок самого клиента (speech.Error с кодом) код есть; у прочих (файл не
// прочитан, адрес не разобран) его даёт общий Classify — тот же разбор, что
// видит человек в проверке ключа.
func classifyCode(err error) core.Code {
	return core.Classify(err, nil).Code
}

// failedRefusal — отказ, по которому повторы кончились: код ядра и класс отказа
// прежние, в текст дописано, почему перестали повторять.
func failedRefusal(err error, since time.Time, note string) error {
	waited := time.Since(since).Round(time.Second)
	return fmt.Errorf("%w (%s; прошло %s)", err, note, waited)
}

// resultText — текст ответа распознавания.
//
// Сплошной текст ответа главнее отрезков: сервис считает его сам. Отрезки —
// запасной путь для ответов с verbose_json, где поля text нет, но так бывает у
// подражателей OpenAI, которые поле недолюбливают.
func resultText(result core.Result) string {
	if text := strings.TrimSpace(result.Text); text != "" {
		return text
	}
	parts := make([]string, 0, len(result.Segments))
	for _, segment := range result.Segments {
		if text := strings.TrimSpace(segment.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
}
