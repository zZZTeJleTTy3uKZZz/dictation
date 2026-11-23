package localdict

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/providercfg"
)

// ПОВТОР ОБЛАЧНОГО РАЗБОРА ПОСЛЕДНЕГО КУСКА.
//
// Обрыв соединения на последнем куске (wsarecv: соединение сброшено) раньше сразу
// кончал диктовку пустым текстом. Теперь разбор той же записи повторяется: всего
// три попытки — между ними паузы 1 с и 3 с, общий срок не больше 30 с. Пока идёт
// разбор, плашка остаётся в «processing»: вставка и «Вставлено» — только после
// непустого текста. Если все попытки неудачны, отказ уходит прежним путём:
// звук остаётся на диске и встаёт в очередь повтора (retry.go).
//
// Повтор не задваивает текст: каждая попытка шлёт ту же запись один раз, а
// результат берётся только у удачной. Отмена человеком (session.cancelled) и
// остановка службы (контекст) прекращают повторы.

// cloudRetryPauses — паузы между попытками: попытка, 1 с, попытка, 3 с, попытка.
var cloudRetryPauses = []time.Duration{time.Second, 3 * time.Second}

// cloudRetryBudget — общий срок всех попыток разбора одной записи.
const cloudRetryBudget = 30 * time.Second

// cloudRetryPoll — как часто пауза между попытками проверяет отмену человеком.
const cloudRetryPoll = 50 * time.Millisecond

// retryableCloud — стоит ли повторять разбор после такого отказа. Повтор не
// поможет, если сервис отказал по существу: ключ не принят, ответ пуст, запись
// пропала, облако не настроено.
func retryableCloud(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errCloudEmpty) || errors.Is(err, errAudioNotFound) {
		return false
	}
	var cloud *cloudError
	if errors.As(err, &cloud) && cloud.cause == nil {
		// Облако не настроено: идти некуда, повтор — пустая трата времени.
		return false
	}
	if providercfg.KindOfCode(classifyRefusal(err)) == providercfg.KindInvalidKey {
		return false
	}
	return true
}

// retryCloudTail — разбор с повторами. attempt делает один запрос; logf пишет
// строку журнала на каждую попытку (reason=ok для удачной). cancelled сообщает,
// что человек отменил запись: тогда повторов уже не будет.
//
// Отдельная функция с подменяемыми аргументами — шов: проверка идёт без сети и
// без настоящих пауз.
func retryCloudTail(
	ctx context.Context,
	cancelled func() bool,
	pauses []time.Duration,
	budget time.Duration,
	attempt func(context.Context) (string, error),
	logf func(n int, reason string),
) (string, error) {
	deadline := time.Now().Add(budget)
	attemptCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	for n := 1; ; n++ {
		text, err := attempt(attemptCtx)
		if err == nil {
			logf(n, "ok")
			return text, nil
		}
		logf(n, shortReason(err))
		if n > len(pauses) || !retryableCloud(err) || cancelled() || ctx.Err() != nil {
			return "", err
		}
		pause := pauses[n-1]
		if time.Until(deadline) <= pause {
			// Срок на все попытки вышел: следующая не успеет дать ответ.
			return "", err
		}
		if !waitCloudRetry(ctx, cancelled, pause) {
			return "", err
		}
	}
}

// waitCloudRetry ждёт паузу между попытками. false — ждать дальше нельзя:
// остановлена служба или человек отменил запись.
func waitCloudRetry(ctx context.Context, cancelled func() bool, pause time.Duration) bool {
	timer := time.NewTimer(pause)
	defer timer.Stop()
	ticker := time.NewTicker(cloudRetryPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return !cancelled() && ctx.Err() == nil
		case <-ticker.C:
			if cancelled() {
				return false
			}
		}
	}
}

// recognizeTailWithRetry разбирает кусок диктовки облаком с повторами и пишет
// журнал по каждой попытке.
func (a *App) recognizeTailWithRetry(session *recordingSession, path, prompt string) (string, error) {
	return retryCloudTail(a.ctx, session.cancelled.Load, cloudRetryPauses, cloudRetryBudget,
		func(ctx context.Context) (string, error) {
			return a.recognizePhraseWithPrompt(ctx, path, prompt)
		},
		func(n int, reason string) {
			a.sessionLog("cloud-retry", fmt.Sprintf("attempt=%d reason=%s", n, reason))
		})
}
