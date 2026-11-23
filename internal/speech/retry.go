// Повторы распознавания — политика поверх клиента ядра speech.
//
// Клиент ядра делает ровно один запрос и отдаёт разобранный отказ speech.Error:
// повторы — дело вызывающего, потому что у проверки ключа и у часовой записи
// они разные (так же устроено в CLI transcribe, speechretry).
//
// ЧТО ПОВТОРЯЕМ. Здесь — только предел частоты 429, и сколько просит сервис.
// Прочее повторяет строка истории своим расписанием (localdict/retry.go): 5xx,
// таймаут и обрыв проходят сами, но ждать их в поле, куда человек только что
// продиктовал, — значит держать его перед пустым полем. Неверный ключ, не тот
// адрес и неподдерживаемый файл от паузы не изменятся вовсе.
//
// ОТКУДА СРОК. Чужой сервис называет срок ожидания предела в Retry-After; ядро
// заголовков не отдаёт. Поэтому клиент заворачивается в HeaderClient: он
// запоминает заголовки ответа под id запроса, а повтор берёт срок ровно того
// ответа, который ждёт (запросы идут параллельно, и «последний ответ» — чужой).
package speech

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	core "github.com/zZZTeJleTTy3uKZZz/dictation/third_party/speech"
)

// retryAfter читает срок из заголовка Retry-After. Он бывает и числом секунд, и
// датой HTTP. Не разобрали — берём запасной срок: повторить сразу значит
// получить второй отказ и потерять запись.
func retryAfter(header string, fallback time.Duration) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return fallback
	}
	if seconds, err := strconv.Atoi(header); err == nil {
		if seconds < 0 {
			return fallback
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(header); err == nil {
		if wait := time.Until(when); wait > 0 {
			return wait
		}
		return 0
	}
	return fallback
}

// sleepWithContext ждёт, но не дольше самого запроса: если время вышло, ждать
// больше нечего.
func sleepWithContext(ctx context.Context, wait time.Duration) bool {
	if wait <= 0 {
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// ── заголовки своего запроса ──────────────────────────────────────────────

// roundTripFunc — транспорт из функции.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip — транспорт из функции.
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// requestIDKey — ключ id запроса в контексте.
type requestIDKey struct{}

// withRequestID — контекст запроса с id, под которым HeaderClient запомнит
// заголовки ответа.
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// requestID — id запроса из контекста; пусто — заголовки не запоминаются.
func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// HeaderClient — HTTP-клиент по образцу base, который запоминает заголовки
// ответа под ключом запроса (id в контексте).
//
// Клиент ядра не отдаёт заголовки (в speech.Error их нет), а срок ожидания
// предела многие сервисы называют только в Retry-After. Ключ — id запроса: так
// заголовки сопоставляются именно с этим ответом, а не с «последним» или «самым
// ранним». Клиент общий на все куски, поэтому запомненное лежит под замком.
func HeaderClient(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	inner := client.Transport
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		transport := inner
		if transport == nil {
			transport = http.DefaultTransport
		}
		resp, err := transport.RoundTrip(r)
		if err == nil && resp != nil {
			if id := requestID(r.Context()); id != "" {
				saveHeaders(id, resp.Header.Clone())
			}
		}
		return resp, err
	})
	return &client
}

// savedHeadersLimit — сколько журналов запомненных ответов держим: ответ нужен
// ровно одной попытке, которая его ждёт; остальное — если ждущий не забрал
// (отменился). Хвост вытесняется по порядку запоминания.
const savedHeadersLimit = 64

var (
	savedHeadersMu sync.Mutex
	savedHeaders   = map[string]http.Header{}
	savedOrder     []string
)

// saveHeaders запоминает заголовки ответа запроса id.
func saveHeaders(id string, header http.Header) {
	savedHeadersMu.Lock()
	defer savedHeadersMu.Unlock()
	if _, seen := savedHeaders[id]; !seen {
		savedOrder = append(savedOrder, id)
	}
	savedHeaders[id] = header
	for len(savedOrder) > savedHeadersLimit {
		delete(savedHeaders, savedOrder[0])
		savedOrder = savedOrder[1:]
	}
}

// takeHeaders отдаёт заголовки ответа на запрос id и забывает их: заголовки
// одного запроса не должны достаться другому. Ответа не было — nil.
func takeHeaders(id string) http.Header {
	savedHeadersMu.Lock()
	defer savedHeadersMu.Unlock()
	header := savedHeaders[id]
	delete(savedHeaders, id)
	return header
}

// requestIDSeq — номера запросов: id уникален и без участия вызывающего.
var requestIDSeq atomic.Uint64

// nextRequestID — новый id запроса.
func nextRequestID() string { return "req-" + strconv.FormatUint(requestIDSeq.Add(1), 10) }

// RetryAfterError — отказ сервиса со сроком, который сервис назвал сам.
//
// Предел частоты (429) называет срок в Retry-After своего ответа, и ждать его
// нужно ровно столько, а не по общему расписанию. Клиент ядра заголовков не
// отдаёт, поэтому срок прикладывается к отказу здесь, а читает его тот, кто
// ставит следующий срок строке истории (localdict/retry.go). Значение в
// сообщении ошибки не участвует: оно нужно решающему, а не читающему.
type RetryAfterError struct {
	Err   error
	After time.Duration
}

// Error — текст отказа как есть.
func (e *RetryAfterError) Error() string { return e.Err.Error() }

// Unwrap отдаёт исходный отказ: errors.As по *speech.Error работает как прежде.
func (e *RetryAfterError) Unwrap() error { return e.Err }

// RetryAfterOf — срок из отказа, названный сервисом; ноль — не назван.
func RetryAfterOf(err error) time.Duration {
	var with *RetryAfterError
	if err != nil && errors.As(err, &with) && with.After > 0 {
		return with.After
	}
	return 0
}

// attachRetryAfter прикладывает Retry-After ответа к отказу ядра.
//
// Только к пределу частоты: срок у прочих отказов в заголовке ничего не
// значит, а пересказывать его расписанию значило бы ждать чужие секунды.
func attachRetryAfter(err error, header http.Header) error {
	if header == nil || classifyCode(err) != core.CodeRateLimited {
		return err
	}
	wait := retryAfter(header.Get("Retry-After"), 0)
	if wait <= 0 {
		return err
	}
	return &RetryAfterError{Err: err, After: wait}
}
