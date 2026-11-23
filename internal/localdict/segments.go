package localdict

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/dictsegment"
)

// segmentQueue — сколько готовых кусков может ждать разбора.
//
// Разбор идёт одним запросом зараз, а кусок длится двадцать-шестьдесят секунд
// речи: за это время облако отвечает несколько раз. Очередь на четыре куска —
// это запас на медленную сеть, а не накопление записи в памяти: длиннее она
// значила бы, что куски копятся быстрее, чем разбираются, и ждать конца всё
// равно пришлось бы при отпускании клавиши.
const segmentQueue = 4

// segmentJob — кусок звука, ушедший в разбор.
type segmentJob struct {
	// index — номер куска по порядку речи. По нему тексты склеиваются даже
	// тогда, когда порядок ответов не совпал бы с порядком отправки.
	index int
	chunk []byte
	// ready — сколько прошло от начала записи до готовности куска. В журнал
	// идёт одно число: и готовность, и конец разбора считаются от этой отметки.
	ready time.Duration
}

// segmentSkip — почему кусок не ушёл в разбор, хотя его и принял разбор.
type segmentSkip int

const (
	// skipNone — кусок разобран, текст есть или пуст по ответу облака.
	skipNone segmentSkip = iota
	// skipSilent — тишина с шипением: в облако не ушёл, ошибки нет.
	skipSilent
	// skipNoise — ровный шум микрофона посреди диктовки: кусок отброшен, сессия
	// продолжается, остальные куски склеиваются как обычно.
	skipNoise
)

// segmentResult — чем кончился разбор куска.
type segmentResult struct {
	index int
	text  string
	err   error
	// skip — кусок не разбирали: тишина или шум (см. segmentSkip). Это не ошибка
	// облака, и повторять его или переходить на разбор целиком незачем.
	skip segmentSkip
	// path — сжатый кусок на диске. При удаче его убирают, при отказе он
	// остаётся для истории: звук не должен пропасть вместе с текстом. Для
	// пропущенного куска путь пуст: его не сжимали.
	path string
	// ready и elapsed — для журнала: готовность куска и сколько шёл разбор.
	ready   time.Duration
	elapsed time.Duration
}

// segmentWorker разбирает куски по одному: одна очередь и один запрос в полёте.
//
// По одному — потому что облако считает предел частоты на все запросы сразу, и
// три параллельных куска получили бы отказ 429 вместо трёх текстов. А ещё так
// порядок текстов совпадает с порядком речи, и склейка не путается.
//
// Хвост предыдущего текста едет подсказкой следующему куску: распознаватель
// видит, на чём кончилась фраза, и не начинает новый кусок будто с чистого
// листа (если способ подсказку принимает — см. channelText).
func (a *App) segmentWorker(ctx context.Context, jobs <-chan segmentJob, results chan<- segmentResult) {
	previous := ""
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobs:
			prompt := dictsegment.PromptTail(previous, dictsegment.PromptLimit)
			began := time.Now()
			text, path, skip, err := a.recognizeSegment(ctx, job.chunk, prompt)
			elapsed := time.Since(began)
			// Пропущенный кусок не меняет подсказку: речь до него и после неё
			// связна, а пустой текст сбросил бы хвост предыдущей фразы.
			if err == nil && skip == skipNone {
				previous = text
			}
			a.sessionLog("timing", fmt.Sprintf(
				"segment-recognized ms=%d index=%d elapsed_ms=%d words=%d failed=%t",
				(job.ready+elapsed).Milliseconds(), job.index, elapsed.Milliseconds(), len(strings.Fields(text)), err != nil))
			select {
			case results <- segmentResult{
				index: job.index, text: text, err: err, skip: skip, path: path,
				ready: job.ready, elapsed: elapsed,
			}:
			case <-ctx.Done():
				// Результат уже некому принять (сессию отменили): сжатый кусок
				// убираем сами, иначе он остался бы на диске навсегда.
				if path != "" {
					_ = os.Remove(path)
				}
				return
			}
		}
	}
}

// recognizeSegment сжимает кусок и разбирает его.
//
// Отказ повторяется один раз: обрыв сети — обычное дело, а терять из-за него
// весь путь нарезки незачем. Второй отказ возвращает ошибку, и сессия
// переходит на прежний путь — разбирает запись целиком, как было до нарезки.
//
// Осмотр звука — тот же, что у разбора целиком: тишина и ровный шум без речи в
// облако не уходят ни куском, ни целой записью. Тишина — не ошибка: кусок
// просто пропускается. Шум посреди диктовки тоже пропускается и не роняет
// сессию; ошибка остаётся только за записью целиком без единого слова (см.
// streamRecording). Сжатия шумового куска нет: его никуда не сохраняют.
func (a *App) recognizeSegment(ctx context.Context, chunk []byte, prompt string) (text, path string, skip segmentSkip, err error) {
	quality := AssessSound(chunk)
	if quality.Silent() {
		a.sessionLog("segment-silent", fmt.Sprintf(
			"rms=%.4f flatness=%.3f low_band=%.3f voiced=%d", quality.RMS, quality.Flatness, quality.LowBand, quality.Voiced))
		return "", "", skipSilent, nil
	}
	if quality.Noisy() {
		a.sessionLog("sound-noise", fmt.Sprintf(
			"rms=%.3f flatness=%.3f low_band=%.3f clipping=%.3f voiced=%d segment=true",
			quality.RMS, quality.Flatness, quality.LowBand, quality.Clipping, quality.Voiced))
		return "", "", skipNoise, nil
	}
	path, err = a.compressChunk(chunk)
	if err != nil {
		return "", "", skipNone, err
	}
	text, err = a.recognizePhraseWithPrompt(ctx, path, prompt)
	if err == nil {
		return strings.TrimSpace(text), path, skipNone, nil
	}
	// Повтор — с той же подсказкой и тем же куском: файл уже сжат и лежит на
	// диске, второй раз сжимать нечего.
	a.sessionLog("segment-retry", fmt.Sprintf("reason=%s", shortReason(err)))
	text, err = a.recognizePhraseWithPrompt(ctx, path, prompt)
	if err != nil {
		return "", path, skipNone, err
	}
	return strings.TrimSpace(text), path, skipNone, nil
}
