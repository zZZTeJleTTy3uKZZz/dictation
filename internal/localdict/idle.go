package localdict

import (
	"time"
)

// Автоостановка по отсутствию голоса.
//
// ПОЧЕМУ. Запись, которую забыли остановить (человек ушёл, микрофон молчит),
// велась бы без конца: звук копится в памяти и на диске, а облако при каждом
// куске получает тишину. Пять минут без голоса — это уже не диктовка, а забытая
// запись. Остановка идёт тем же путём, что и отпускание клавиши: то, что
// наговорено, разбирается и вставляется. Отличается только итог — статус
// autostop, а не ошибка.
//
// ЧТО СЧИТАЕТСЯ ГОЛОСОМ. Тот же порог, что у звучащего кадра (soundActiveRMS и
// HasSpeech): уровень последнего буфера микрофона не ниже него. Тихое шипение
// (RMS 0,001 из журнала) голосом не считается и паузу не продлевает.
//
// Час в проверке подменяется (App.clock), так что тест не ждёт пяти минут.

// voiceIdleLimit — сколько без голоса терпит запись, прежде чем остановиться.
const voiceIdleLimit = 5 * time.Minute

// autostopReason — причина в истории для записи, остановленной без единого
// слова. Это пояснение, а не ошибка: статус у такой записи — autostop.
const autostopReason = "5 минут без голоса — остановлено в фоне"

// voiceWatch считает время от последнего такта, где был голос.
type voiceWatch struct {
	limit time.Duration
	last  time.Time
}

// newVoiceWatch начинает отсчёт с момента старта записи: сразу после старта
// пауза уже есть, но до пяти минут ещё далеко.
func newVoiceWatch(limit time.Duration, start time.Time) *voiceWatch {
	return &voiceWatch{limit: limit, last: start}
}

// update принимает уровень звука на этом такте и время такта. Возвращает, сколько
// прошло без голоса к этому моменту.
func (w *voiceWatch) update(level float64, now time.Time) time.Duration {
	if level >= soundActiveRMS {
		w.last = now
	}
	return now.Sub(w.last)
}

// expired — пора ли останавливать запись: пауза достигла предела.
func (w *voiceWatch) expired(idle time.Duration) bool {
	return idle >= w.limit
}
