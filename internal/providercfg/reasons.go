package providercfg

import (
	"context"
	"errors"
	"net"

	core "github.com/zZZTeJleTTy3uKZZz/dictation/third_party/speech"
)

// Причины отказа распознавания — на общем ядре speech.
//
// ЗАЧЕМ ЭТО ОСТАЛОСЬ. Один и тот же отказ объясняют два места: окно проверяет
// ключ, а строка истории несёт причину рядом с записью, которая не
// расшифровалась. Слова у них теперь одни — Verdict ядра (код, короткая причина
// и совет): пока слова жили в двух местах, они уже разошлись — «ключ не принят»
// против «ключ не принят сервисом» про один и тот же 401.
//
// ЧТО ЗДЕСЬ ЕСТЬ. Виды окна (Kind*), отложения Диктовки от причины ядра
// (VerdictOf) и разбор отказа без тела ответа для строки истории
// (VerdictForStatus). Самого словаря причин больше нет — он в ядре.

// KindOfCode — вид окна по коду ядра.
func KindOfCode(code core.Code) string {
	kind, _ := kindOf(code)
	return kind
}

// Transient — повторять ли строку истории после этого отказа.
//
// ОТКАЗ, КОТОРЫЙ ПРОЙДЁТ САМ. 5xx, 408 и 425, таймаут, срыв сети и «сервис
// недоступен» — это не про настройки человека, а про мгновение: сервис перегружен,
// сеть мигнула, посредник не принял запрос. Такая строка ждёт следующего срока
// (retrying), а не становится окончательной ошибкой. Прежний разбор сводил всё
// это к «сервис занят» (429), и работавший на e67772e автоповтор для 5xx был
// потерян.
//
// 401 и 403 не повторяются никогда: ключ от паузы не исправится, а строка,
// которая «сама пройдёт», не покажет человеку, что нужно вмешаться.
func Transient(err error) (retry bool) {
	if err == nil {
		return false
	}
	var failed *core.Error
	if errors.As(err, &failed) {
		// 401 — ключ; 403 — либо ключ, либо запрет по сети (прокси), и то и
		// другое само не пройдёт.
		if failed.Status == 401 || failed.Status == 403 {
			return false
		}
		switch {
		case failed.Status == 429, failed.Status == 408, failed.Status == 425:
			return true
		case failed.Status >= 500:
			return true
		case failed.Status == 0 && (failed.Code == core.CodeTimeout || failed.Code == core.CodeTLS):
			// До ответа дело не дошло: таймаут или TLS. Повторяемы оба.
			return true
		case failed.Code == core.CodeUnavailable:
			return true
		}
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Не ошибка клиента вовсе (обрыв до отправки, DNS, отмена по сроку).
	return isTransport(err)
}

// isTransport — до сервиса не дошли: обрыв, DNS, таймаут.
func isTransport(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dns *net.DNSError
	var op *net.OpError
	return errors.As(err, &dns) || errors.As(err, &op)
}

// Verdict — причина словами для человека по виду окна.
//
// Нужно там, где известен только вид: снимок настроек, журнал, старые записи
// истории. Неизвестный вид — «сервис отказал»: пустая строка вместо объяснения
// хуже неточной.
func Verdict(kind string) core.Verdict {
	return guarded(codeOf(kind))
}

// VerdictOf — причина словами для человека по виду окна (или по коду ядра).
//
// Нужно там, где известен только вид: снимок настроек, журнал, старые записи
// истории. Неизвестный вид — «сервис отказал»: пустая строка вместо объяснения
// хуже неточной.
func VerdictOf(kind core.Code) core.Verdict {
	return guarded(kind)
}

// VerdictForStatus — причина по коду ответа, без разбора тела. Так её видит
// история: тело ответа до неё не доходит, да и не нужно — запись сохранена, и
// человеку важно знать, чинить ли ключ, ждать или искать сеть.
//
// 401 и 403 у истории — ключ: запрет по стране окно отличает по телу ответа, и
// только оно (см. result проверки).
func VerdictForStatus(status int) core.Verdict {
	switch status {
	case 401, 403:
		return guarded(core.CodeKeyRejected)
	case 408:
		return guarded(core.CodeTimeout)
	case 429:
		return guarded(core.CodeRateLimited)
	default:
		if status >= 500 {
			return guarded(core.CodeUnavailable)
		}
		return guarded(core.CodeBadResponse)
	}
}

// guarded — слова ядра по коду, но без опоры на его нулевое значение.
//
// Итог ядра собирается из отказа, а отказа у нас нет: здесь известен только
// код, и запрос за словами — пустой Classify ничего не знающей ошибки.
// Свой список причин — это вторая копия словаря, и разошлась бы она с ядром на
// первой же правке чужих слов.
func guarded(code core.Code) core.Verdict {
	if code == "" {
		return core.Verdict{}
	}
	return core.Classify(&core.Error{Code: code}, nil)
}

// kindOfCode — вид окна по коду ядра. Виды и коды — не одно и то же: у окна их
// девять, у ядра семь, и «сеть» с «сервером» в ядре одна «недоступность».
func codeOf(kind string) core.Code {
	switch kind {
	case KindOK:
		return ""
	case KindInvalidKey:
		return core.CodeKeyRejected
	case KindBlocked:
		return core.CodeRegionBlocked
	case KindBusy:
		return core.CodeRateLimited
	case KindTimeout:
		return core.CodeTimeout
	case KindTLS:
		return core.CodeTLS
	case KindNetwork, KindServer:
		return core.CodeUnavailable
	default:
		return core.CodeBadResponse
	}
}
