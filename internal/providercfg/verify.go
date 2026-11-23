// Проверка ключа и адреса настоящим запросом — на общем ядре speech/conn.
//
// ЗАЧЕМ ПО-НАСТОЯЩЕМУ. «Ключ сохранён» ничего не говорит: он может быть
// отозван, скопирован с лишним пробелом или просто не принят сервисом. Человек
// узнает об этом либо здесь, либо посреди диктовки — и потеряет фразу. Поэтому
// кнопка «Проверить» отправляет короткий встроенный образец на
// /audio/transcriptions того же сервиса, куда пойдёт настоящая запись, и
// называет итог словами.
//
// ЧТО ЗДЕСЬ ОСТАЛОСЬ. Сам запрос — conn.Check: тот же клиент ядра, тот же
// образец (conn.SampleWAV), тот же разбор отказа (speech.Verdict) и та же
// проверка записи провайдера, что у CLI transcribe. Здесь только слова Диктовки:
// вид итога, текст для окна и признак «предложить прокси» — из Verdict.
//
// ПОЧЕМУ ОБРАЗЕЦ, А НЕ СПИСОК МОДЕЛЕЙ. Список моделей есть не у всех
// OpenAI-совместимых сервисов, и «нет такой пути» ещё не значит «ключ плохой».
// Запрос расшифровки — ровно тот, которым живёт диктовка: если он прошёл, значит
// пройдёт и настоящий.
package providercfg

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/speech"
	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/speech/conn"
)

// Виды итогов проверки. Имена уходят в окно Flutter — контракт JSON окна: по ним
// оно решает, что показать — «ключ не принят» или «запрос не доходит из вашей
// сети». Значения обязаны остаться прежними, даже если коды ядра иные.
const (
	KindOK         = "ok"
	KindInvalidKey = "invalid-key"
	KindBlocked    = "blocked"
	KindTimeout    = "timeout"
	KindTLS        = "tls"
	KindNetwork    = "network"
	KindBusy       = "busy"
	KindServer     = "server"
	KindRefused    = "refused"
)

// checkTimeout — сколько ждём ответа на проверку. Настоящая запись разбирается
// минутами, а «ты здесь?» должен отвечать за секунды: иначе человек решит, что
// висит окно, а не сеть. Передаётся ядру через срок записи провайдера не
// выходит — у conn.Check свой, тот же по величине.
var checkTimeout = 20 * time.Second

// CheckRequest — что и куда проверяем.
type CheckRequest struct {
	// URL — адрес OpenAI-совместимого API (без хвоста расшифровки или с ним).
	URL string
	// Key, Model, Language — то, что человек вписал в окне.
	Key      string
	Model    string
	Language string
	// Proxy — строка выхода (адрес, имя и пароль — внутри адреса). Через он же
	// собирается транспорт: проверка идёт тем же путём, что настоящая диктовка.
	Proxy string
	// Timeout — свой срок проверки; ноль — checkTimeout. Это срок на сборку
	// клиента: сам ответ ядро ждёт по своему сроку.
	Timeout time.Duration
}

// CheckResult — итог проверки словами для человека.
type CheckResult struct {
	OK   bool   `json:"ok"`
	Kind string `json:"kind"`
	// Note — что сказать человеку. Пишется здесь, а не в окне: правило
	// «403 без слова invalid key — это не ключ, а сеть» живёт в одном месте —
	// словаре ядра (speech.Verdict), — иначе окно и служба начнут объяснять
	// один ответ по-разному.
	Note string `json:"note"`
	// Detail — короткий ответ сервиса (или причина отказа сети) для тех, кому
	// мало общего объяснения.
	Detail string `json:"detail,omitempty"`
	// Proxy — стоит ли предложить прокси. Истина и для отказа ключа? Нет: при
	// неверном ключе прокси ничего не изменит, и предлагать его — сбивать с
	// толку.
	Proxy bool `json:"proxy,omitempty"`
}

// Check отправляет образец на сервис и объясняет итог.
//
// Время проверки — здесь: ядро считает отмену и истёкший срок решением
// вызывающего и отдаёт их ошибкой, а человеку, который нажал «Проверить»,
// нужно «сервис не ответил вовремя» и совет про прокси — ровно как раньше.
func Check(ctx context.Context, req CheckRequest) CheckResult {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = checkTimeout
	}
	provider := conn.Provider{
		BaseURL: strings.TrimSpace(req.URL),
		Model:   strings.TrimSpace(req.Model),
		Proxy:   strings.TrimSpace(req.Proxy),
	}
	// Запись проверяется целиком до запроса: пустой адрес, сеть по http,
	// опечатка в модели или в прокси — это «исправьте настройку», а не отказ
	// сервиса, и запрос в такой проверке уходить не должен вовсе.
	if _, err := provider.Normalize(); err != nil {
		return refused(err)
	}
	ask, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	accepted, refusal := checkProvider(ask, provider, strings.TrimSpace(req.Key), strings.TrimSpace(req.Language))
	if accepted {
		return CheckResult{OK: true, Kind: KindOK, Note: "сервис принял запрос — ключ работает"}
	}
	switch {
	case errors.Is(refusal, context.DeadlineExceeded) || errors.Is(refusal, context.Canceled) && ask.Err() != nil:
		// Решили подождать и не дождались — это молчание сервиса, а не ошибка
		// настройки.
		if ctx.Err() != nil {
			return refused(ctx.Err())
		}
		return result(guarded(speech.CodeTimeout))
	}
	// Ошибка значит «проверить нельзя»: неверная запись, строка выхода или
	// отмена. Отказ сервиса приходит не ошибкой, а разобранным отказом ядра.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return refused(ctxErr)
	}
	return resultFor(refusal)
}

// checkProvider — пробный запрос так же, как его делает диктовка: формой на
// /audio/transcriptions, с ключом, моделью и языком.
//
// Язык едет в проверку не для красоты: без него короткий образец принимают за
// английский, и проверка на своей речи отвечала бы не тем, что ждёт человек.
// Пустое поле значит «не назван»: запрос уходит без поля language.
//
// Пробелы по краям язык убирает ВЫЗЫВАЮЩИЙ (Check): здесь значение уже то, что
// уйдёт в запрос. «ru » из voice.json не должно превращаться в пустое поле — а
// оно бы и превратилось, чисти язык только здесь: checkProvider сначала щёлкнул
// бы условие пустоты, а обрезал уже после.
//
// Возвращает принят ли запрос и, если нет, отказ целиком — с HTTP-кодом ответа.
// Отказ сервиса (401, 429, 500) приходит отказом, а не ошибкой; ошибкой идут
// только «проверить нельзя» (неверная запись, прокси) и отмена. Разбор по
// коду ответа нужен потому, что ядро сводит 5xx и срыв сети в одну
// «недоступность», а окну их объяснять надо по-разному (resultFor).
func checkProvider(ctx context.Context, provider conn.Provider, key, language string) (bool, error) {
	normalized, err := provider.Normalize()
	if err != nil {
		return false, fmt.Errorf("проверка не отправлена: %w", err)
	}
	if strings.TrimSpace(key) == "" {
		// Пустой ключ в запрос не уходит вовсе: без него сервис ответил бы 401,
		// и человек искал бы причину не там.
		return false, &speech.Error{Code: speech.CodeKeyRejected, Msg: "ключ не указан"}
	}
	transport, err := conn.NewTransport(normalized.Proxy)
	if err != nil {
		return false, fmt.Errorf("прокси: %w", err)
	}
	client, err := speech.New(speech.Config{
		BaseURL: normalized.BaseURL,
		Key:     strings.TrimSpace(key),
		Model:   normalized.Model,
		HTTP:    &http.Client{Transport: transport, Timeout: checkTimeout},
	})
	if err != nil {
		return false, err
	}
	_, err = client.Transcribe(ctx, speech.Request{
		Audio:    conn.SampleWAV(),
		Filename: "check.wav",
		Language: language,
	})
	if err == nil {
		return true, nil
	}
	return false, err
}

// result — итог ядра словами Диктовки.
//
// Код ядра переводится в вид окна, текст берётся из Verdict как есть: совет
// человеку у одного и того же отказа должен звучать одинаково у окна, у CLI и в
// строке истории.
func result(verdict speech.Verdict) CheckResult {
	if verdict.OK() {
		return CheckResult{OK: true, Kind: KindOK, Note: verdict.Reason}
	}
	kind, proxy := kindOf(verdict.Code)
	note := verdict.Reason
	if verdict.Advice != "" {
		note = joinSentences(verdict.Reason, verdict.Advice)
	}
	if note == "" {
		// Пустого объяснения человеку не показывают: у кода ядра есть свои
		// слова, и они понятнее общего «не удалось».
		note = VerdictOf(verdict.Code).Reason
	}
	return CheckResult{Kind: kind, Note: note, Proxy: proxy}
}

// resultFor — итог по отказу ядра, а не по одному коду.
//
// 5xx — это «сервис ответил ошибкой» (server), а не «нет связи» (network):
// прокси тут ничего не исправит, и совет про него увёл бы человека чинить
// посредника, который ни при чём. Прокси предлагается только там, где он
// помогает: сеть, TLS, таймаут и запрет по стране/сети (403 без слова о ключе).
// Именно поэтому результат берётся по HTTP-коду ответа, а не по коду ядра,
// который 5xx с сетевым срывом сводит в одно.
func resultFor(err error) CheckResult {
	if err == nil {
		return CheckResult{OK: true, Kind: KindOK, Note: "сервис принял запрос — ключ работает"}
	}
	var failed *speech.Error
	if errors.As(err, &failed) && failed.Status >= 500 {
		note := joinSentences("сервис ответил ошибкой", "Повторите позже: прокси на 5xx не поможет.")
		detail := failed.Detail
		if detail == "" {
			detail = failed.Msg
		}
		return CheckResult{Kind: KindServer, Note: note, Detail: detail}
	}
	return result(speech.Classify(err, nil))
}

// kindOf — вид окна и совет про прокси по коду ядра.
//
// Совет про прокси — свойство отказа, а не вида: 5xx от нашего же сервиса
// посредником не лечится (это вид «сервис ответил ошибкой» в старом словаре
// причин), а TLS и таймаут лечатся.
func kindOf(code speech.Code) (kind string, proxy bool) {
	switch code {
	case speech.CodeKeyRejected:
		return KindInvalidKey, false
	case speech.CodeRegionBlocked:
		return KindBlocked, true
	case speech.CodeRateLimited:
		return KindBusy, false
	case speech.CodeTimeout:
		return KindTimeout, true
	case speech.CodeTLS:
		return KindTLS, true
	case speech.CodeUnavailable:
		// «Недоступность» у ядра одна на 5xx и на срыв сети. Вид для окна
		// выбирает тот, кто видел код ответа: resultFor.
		return KindNetwork, true
	case speech.CodeBadResponse:
		return KindRefused, false
	default:
		return KindRefused, false
	}
}

// refused — проверка не состоялась: настройку исправлять, а не сеть.
func refused(err error) CheckResult {
	return CheckResult{
		Kind: KindRefused,
		Note: "Проверка не отправлена: " + sentence(err.Error()),
	}
}

// joinSentences — две короткие фразы в одно объяснение: причина и совет.
func joinSentences(reason, advice string) string {
	reason, advice = strings.TrimSpace(reason), strings.TrimSpace(advice)
	switch {
	case reason == "":
		return advice
	case advice == "":
		return reason
	}
	if strings.HasSuffix(reason, ".") || strings.HasSuffix(reason, "!") || strings.HasSuffix(reason, "?") {
		return reason + " " + advice
	}
	return reason + ". " + advice
}

// sentence — сообщение как есть, но пустое называет себя: «Проверка не
// отправлена: » читается как оборванная фраза.
func sentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "причина не названа"
	}
	return text
}
