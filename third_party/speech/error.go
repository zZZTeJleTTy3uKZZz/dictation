package speech

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Code — машинный код отказа распознавания. Набор общий для CLI, окна и
// службы: по нему выбирается код выхода, текст и следующий шаг. Меняется только
// вместе с потребителями.
type Code string

const (
	// CodeKeyRejected — сервис не принял ключ (401, а также 403, где виноват ключ).
	CodeKeyRejected Code = "key_rejected"
	// CodeRegionBlocked — сервис не пускает из этой сети или страны (403 без слов о ключе).
	CodeRegionBlocked Code = "region_blocked"
	// CodeRateLimited — предел запросов (429).
	CodeRateLimited Code = "rate_limited"
	// CodeTimeout — ответа не дождались: срок или сеть.
	CodeTimeout Code = "timeout"
	// CodeTLS — рукопожатие или проверка сертификата не состоялись.
	CodeTLS Code = "tls"
	// CodeUnavailable — 5xx или сеть недоступна без таймаута и TLS.
	CodeUnavailable Code = "unavailable"
	// CodeBadResponse — ответ пришёл, но не разобрался либо отказ по существу
	// (400, 404, 413, 422).
	CodeBadResponse Code = "bad_response"
)

// Verdict — что случилось и что с этим делать, словами для человека.
//
// Reason — одна короткая фраза о случившемся, Advice — одна короткая фраза о
// следующем шаге. Подробности (код ответа, тело) остаются в Error: в окне и
// строке истории нужны короткие слова, а не пересказ ответа сервиса.
type Verdict struct {
	Code   Code   `json:"code"`
	Reason string `json:"reason"`
	Advice string `json:"advice"`
}

// OK — отказа не было.
func (v Verdict) OK() bool { return v.Code == "" }

// Error — отказ распознавания с машинным кодом и причиной.
type Error struct {
	// Code — машинный код; всегда заполнен.
	Code Code
	// Status — HTTP-код сервиса; 0 — сеть или разбор.
	Status int
	// Msg — что случилось, для журнала и подробного вывода. Ключ замаскирован.
	Msg string
	// Detail — короткое тело ответа сервиса (до 400 байт, ключ замаскирован).
	Detail string
	// Cause — исходная ошибка сети, TLS или таймаута. В текст не выводится.
	Cause error
}

func (e *Error) Error() string { return e.Msg }

// Unwrap отдаёт причину: errors.Is и errors.As работают и для сетевых классов.
func (e *Error) Unwrap() error { return e.Cause }

// Verdict — короткие слова по этому отказу.
func (e *Error) Verdict() Verdict {
	return verdictFor(e.Code)
}

// Classify — итог отказа: по ошибке клиента (err) либо по ответу сервиса (resp).
//
// Ровно одно из двух обычно задано; если заданы оба, главнее err. Отмена
// (context.Canceled) — не отказ сервиса, а решение вызывающего: для неё итог
// пустой (Verdict.OK()). Ответ читается не больше 64 КиБ и закрывается:
// вызывающему он уже не нужен, а тело без разбора — это утечка и мусор в журнал.
func Classify(err error, resp *http.Response) Verdict {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return Verdict{}
		}
		var failed *Error
		if errors.As(err, &failed) {
			if failed.Code != "" {
				return verdictFor(failed.Code)
			}
			return verdictFor(codeForCause(failed.Cause))
		}
		return verdictFor(codeForCause(err))
	}
	if resp != nil {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		return verdictFor(codeForStatus(resp.StatusCode, raw))
	}
	return Verdict{}
}

// verdictFor — короткие русские фразы по коду.
func verdictFor(code Code) Verdict {
	switch code {
	case CodeKeyRejected:
		return Verdict{Code: code, Reason: "ключ не принят сервисом",
			Advice: "Проверьте ключ и впишите его заново."}
	case CodeRegionBlocked:
		return Verdict{Code: code, Reason: "сервис недоступен из этой сети",
			Advice: "Укажите прокси и повторите."}
	case CodeRateLimited:
		return Verdict{Code: code, Reason: "сервис занят: слишком много запросов",
			Advice: "Повторите через минуту."}
	case CodeTimeout:
		return Verdict{Code: code, Reason: "сервис не ответил вовремя",
			Advice: "Проверьте сеть или укажите прокси."}
	case CodeTLS:
		return Verdict{Code: code, Reason: "защищённое соединение не установилось",
			Advice: "Проверьте прокси и часы на машине."}
	case CodeUnavailable:
		return Verdict{Code: code, Reason: "нет связи с сервисом",
			Advice: "Проверьте сеть или укажите прокси."}
	case CodeBadResponse:
		return Verdict{Code: code, Reason: "сервис ответил непонятно",
			Advice: "Проверьте адрес API и модель."}
	default:
		return Verdict{Code: code, Reason: "распознавание не удалось",
			Advice: "Проверьте настройки и повторите."}
	}
}

// codeForStatus — класс отказа по HTTP-коду и телу ответа.
//
// Порядок проверок не случаен: 401 — это всегда ключ; 403 бывает и ключом, и
// запретом по стране, и советы человеку разные («впишите другой ключ» против
// «нужен прокси»), поэтому 403 разбирается по телу.
func codeForStatus(status int, body []byte) Code {
	switch {
	case status >= 200 && status < 300:
		return ""
	case status == http.StatusUnauthorized:
		return CodeKeyRejected
	case status == http.StatusForbidden:
		if saysInvalidKey(body) {
			return CodeKeyRejected
		}
		return CodeRegionBlocked
	case status == http.StatusRequestTimeout:
		return CodeTimeout
	case status == http.StatusTooManyRequests:
		return CodeRateLimited
	case status >= 500:
		return CodeUnavailable
	default:
		return CodeBadResponse
	}
}

// codeForCause — класс отказа по причине, до сервиса не дошли.
//
// Различаем по типам, а не по подстрокам текста: текст ошибки зависит от сети,
// Go и посредника. Истёкший срок — context.DeadlineExceeded или net.Error с
// Timeout(); сбой TLS — типы crypto/tls и crypto/x509; всё остальное (обрыв,
// отказ, DNS) — недоступность: отдельного класса для неё нет.
func codeForCause(err error) Code {
	switch {
	case err == nil:
		return CodeUnavailable
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return CodeTimeout
	case isTLSError(err):
		return CodeTLS
	default:
		return CodeUnavailable
	}
}

// isTimeout — сработал сетевой таймаут.
func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// isTLSError — сбой рукопожатия или проверки сертификата.
//
// Типы: неизвестный удостоверяющий центр, чужое имя, негодный сертификат,
// проверка сертификата, битая запись рукопожатия. Строку ошибки не разбираем.
func isTLSError(err error) bool {
	var verify *tls.CertificateVerificationError
	if errors.As(err, &verify) {
		return true
	}
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		return true
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return true
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		return true
	}
	var record tls.RecordHeaderError
	return errors.As(err, &record)
}

// truncate обрезает текст до limit байт по границе руны и помечает обрезку.
//
// Резать по байтам нельзя: тело ответа бывает по-русски, и разрез посреди
// буквы даёт битую строку — в журнале, в окне и в JSON.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// saysInvalidKey — сказал ли сервис, что дело в ключе.
func saysInvalidKey(body []byte) bool {
	said := strings.ToLower(string(body))
	for _, phrase := range []string{
		"invalid key", "invalid api key", "invalid_api_key", "incorrect api key",
		"invalid key provided", "api key is invalid", "bad api key", "неверный ключ",
	} {
		if strings.Contains(said, phrase) {
			return true
		}
	}
	return false
}
