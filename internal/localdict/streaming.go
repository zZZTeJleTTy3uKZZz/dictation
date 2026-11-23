package localdict

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/dictsegment"
)

type recordingSession struct {
	stop      chan struct{}
	stopOnce  sync.Once
	cancelled atomic.Bool
	// interrupted — сессию закрыло выключение Windows: звук дописан и остаётся
	// на диске, а разбор не ждётся. Отмена (cancelled) — другое: её просил
	// человек, и запись ему не нужна.
	interrupted atomic.Bool
	// autostop — запись остановила сама пауза без голоса (idle.go), а не человек.
	// Итог помечается как autostop, и ошибкой он не бывает.
	autostop atomic.Bool
	// started — момент начала сессии: от него считается пауза без голоса.
	started time.Time
	journal *journal
	// historySaved — итог разговора уже лёг в history.json. Журнал получает
	// метку finish только после этого: сбой между двумя записями не должен
	// оставить разговор без строки в истории.
	historySaved bool
}

// end закрывает канал остановки один раз: сессию останавливают и человек, и
// выключение Windows, и обе просьбы могут прийти одновременно.
func (s *recordingSession) end() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (a *App) streamRecording(session *recordingSession, target Target) {
	defer a.work.Done()
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	transcript := ""
	a.sessionLog("recording-start", fmt.Sprintf("target=%t %s", target.Window != 0, targetDescription(target)))
	stop := session.stop
	final := false
	var previousWords []string
	committed := 0
	// НАРЕЗКА ПО ПАУЗАМ. Звук режется на куски по паузам и разбирается в фоне,
	// пока человек говорит: к отпусканию клавиши остаётся только хвост после
	// последнего куска, а не вся запись.
	segJobs := make(chan segmentJob, segmentQueue)
	segResults := make(chan segmentResult, segmentQueue)
	segCtx, segCancel := context.WithCancel(a.ctx)
	segDone := make(chan struct{})
	go func() { defer close(segDone); a.segmentWorker(segCtx, segJobs, segResults) }()
	defer func() {
		segCancel()
		<-segDone
		// Кусок, не дошедший до разбора, не должен остаться на диске навсегда:
		// временные файлы убирает тот, кто их завёл.
		for {
			select {
			case result := <-segResults:
				if result.path != "" {
					_ = os.Remove(result.path)
				}
			default:
				return
			}
		}
	}()
	// segmentSent — сколько кусков ушло в разбор; segmentWaiting — сколько ещё
	// не вернулось; segmentNext — номер куска, ждущего склейки.
	segmentSent, segmentWaiting, segmentNext := 0, 0, 0
	segmentReady := make(map[int]string, segmentQueue)
	segmentTexts := make([]string, 0, segmentQueue)
	// segmentFallback — кусок не разобрался и после повтора: запись уходит в
	// разбор целиком, как было до нарезки.
	segmentFallback := false
	// watch следит за паузой без голоса: пять минут тишины — автоостановка.
	// Отсчёт тишины идёт от старта сессии, а не от старта этой горутины: иначе
	// часы, поставленные сразу после Begin, успевали бы уйти вперёд.
	watchStart := session.started
	if watchStart.IsZero() {
		watchStart = a.timeNow()
	}
	watch := newVoiceWatch(voiceIdleLimit, watchStart)
	// noiseSkipped — сколько кусков отброшено как шум микрофона. Имеет значение
	// только в конце: если слов не нашлось совсем, вход испорчен, а не тихий.
	noiseSkipped := 0
	// recognizedChunks — сколько кусков (и хвост) ушли в распознавание и
	// вернулись без ошибки, с текстом или пустым. Если хоть один ушёл, в звуке
	// была речь, и пустой итог — это тишина, а не испорченный вход.
	recognizedChunks := 0
	// finalAudio — звук, оставшийся к отпусканию клавиши, уже взят на разбор.
	// Без этой отметки цикл, который ждёт вставки или правки, брал бы запасной
	// путь заново на каждом обороте и разбирал бы всю запись по второму разу.
	finalAudio := false
	var releasedAt time.Time
	var lastSegmentCheck time.Time
	// acceptSegment принимает разобранный кусок: удачный встаёт в очередь на
	// склейку, пропущенный (тишина или шум) — пустым текстом, неудачный
	// переводит сессию на прежний путь. Сессия при этом не останавливается.
	acceptSegment := func(result segmentResult) {
		segmentWaiting--
		if result.err != nil {
			a.sessionLog("segment-failed", fmt.Sprintf(
				"index=%d retried=true elapsed_ms=%d reason=%s",
				result.index, result.elapsed.Milliseconds(), shortReason(result.err)))
			if result.path != "" {
				_ = os.Remove(result.path)
			}
			if !segmentFallback {
				segmentFallback = true
				a.sessionLog("segment-fallback", "reason=segment-not-recognized")
			}
			return
		}
		if result.path != "" {
			_ = os.Remove(result.path)
		}
		text := result.text
		if result.skip == skipNone {
			recognizedChunks++
		}
		if result.skip == skipNoise {
			// Шум посреди диктовки: кусок отброшен, звук не сохраняется — это
			// не речь и не запись, которую человек хотел бы перепроверить.
			// Остальные куски склеиваются как обычно.
			noiseSkipped++
			a.sessionLog("segment-noise-skipped", fmt.Sprintf(
				"index=%d elapsed_ms=%d", result.index, result.elapsed.Milliseconds()))
			text = ""
		}
		segmentReady[result.index] = text
		// Тексты склеиваются по порядку номеров, а не по порядку ответов: даже
		// если ответы придут не по очереди, речь останется в своём порядке.
		for {
			text, ok := segmentReady[segmentNext]
			if !ok {
				break
			}
			delete(segmentReady, segmentNext)
			segmentNext++
			if text == "" {
				continue
			}
			segmentTexts = append(segmentTexts, text)
		}
	}
	for {
		// Выключение машины проверяется первым: его сессия не разбирается и не
		// удаляется, а отмена человеком — удаляется.
		if session.interrupted.Load() {
			a.completeSession(session, "interrupted", transcript, nil)
			return
		}
		if session.cancelled.Load() || a.ctx.Err() != nil {
			a.completeSession(session, "cancelled", transcript, nil)
			return
		}
		// Автоостановка: пять минут без голоса. Запись кончается так же, как при
		// отпускании клавиши — итог разбирается и вставляется, — но помечается
		// autostop. Закрытие канала stop будит цикл ниже, сам цикл не трогаем.
		if !final {
			idle := watch.update(a.audio.Level(), a.timeNow())
			if watch.expired(idle) {
				a.stopForVoiceIdle(session, idle)
			}
		}
		// Готовые куски принимаются на каждом обороте: разбор идёт в фоне и
		// записи не мешает. Ошибка куска решает, разбирать ли запись целиком,
		// и узнать о ней лучше сразу, а не при отпускании клавиши.
		for drained := false; !drained; {
			select {
			case result := <-segResults:
				acceptSegment(result)
			default:
				drained = true
			}
		}
		// Готовый кусок уходит в разбор сразу, как накопился: человек ещё
		// говорит, а начало записи уже разбирается. Проверка «есть ли место в
		// очереди» идёт ДО того, как звук отдан: отданный кусок вернуть в буфер
		// нельзя, и при полной очереди он пропал бы. Раз в полсекунды, а не на
		// каждом обороте: поиск паузы проходит по накопленному звуку, и делать
		// это двенадцать раз в секунду незачем.
		if !final && !segmentFallback && len(segJobs) < cap(segJobs) &&
			time.Since(lastSegmentCheck) >= 500*time.Millisecond {
			lastSegmentCheck = time.Now()
			if ready := a.audio.NextSegment(); len(ready) != 0 {
				a.sessionLog("timing", fmt.Sprintf("segment-ready ms=%d bytes=%d",
					time.Since(a.began).Milliseconds(), len(ready)))
				if HasSpeech(ready) {
					index := segmentSent
					segmentSent++
					segmentWaiting++
					segJobs <- segmentJob{index: index, chunk: ready, ready: time.Since(a.began)}
				} else {
					// Тишина: разбирать нечего, и звук отпускается, чтобы не
					// копиться до конца разговора.
					a.sessionLog("timing", fmt.Sprintf("segment-silent bytes=%d", len(ready)))
				}
			}
		}
		var chunk []byte
		tookFinal := false
		if final && !finalAudio {
			tookFinal, finalAudio = true, true
			// Отпускание клавиши: дожидаемся кусков, ушедших в разбор, а
			// сами разбираем только хвост после последнего куска. Если кусок
			// не разобрался и после повтора — прежний путь: вся запись
			// целиком.
			for segmentWaiting > 0 {
				select {
				case result := <-segResults:
					acceptSegment(result)
				case <-a.ctx.Done():
					return
				case <-ticker.C:
					// Выключение машины не ждёт облака: звук уже на диске,
					// и разберут его при следующем запуске.
					if session.interrupted.Load() {
						a.completeSession(session, "interrupted", "", nil)
						return
					}
				}
			}
			if segmentFallback {
				chunk = a.audio.All()
			} else {
				// Разобранные куски становятся началом текста, а хвост
				// приклеится к ним ниже, в общем порядке речи. В журнал
				// слова уходят только вместе с хвостом: отмена и выключение
				// машины закрывают журнал без итога, и разговор, который
				// человек отменил, не должен всплыть в истории половиной
				// текста.
				transcript = dictsegment.Join(segmentTexts...)
				chunk = a.audio.NextChunk(true)
			}
		}
		phraseFinal := len(chunk) != 0
		// Тишина с шипением — не речь и не шум: до облака такой хвост не доходит
		// и ошибки не даёт. Проверка идёт до шумовой, у тишины шум не ищем.
		speech := HasSpeech(chunk)
		var quality SoundQuality
		if speech {
			quality = AssessSound(chunk)
			if quality.Silent() {
				a.sessionLog("timing", fmt.Sprintf("tail-silent bytes=%d rms=%.4f voiced=%d", len(chunk), quality.RMS, quality.Voiced))
				speech = false
			}
		}
		if speech && quality.Noisy() {
			// Испорченный вход: ровный шум без речи. Распознаватель на нём либо
			// молчит, либо выдумывает слова, а запрос в облако всё равно платный.
			a.sessionLog("sound-noise", fmt.Sprintf(
				"rms=%.3f flatness=%.3f low_band=%.3f clipping=%.3f voiced=%d",
				quality.RMS, quality.Flatness, quality.LowBand, quality.Clipping, quality.Voiced))
			if recognizedChunks > 0 && !segmentFallback {
				// Хвост после удачно распознанных кусков оказался шумом: прежний
				// путь — вся запись целиком. Иначе текст кусков остался бы в строке
				// ошибки, а хвост отдельной строкой failed, и склеить их было бы
				// нечем. Шум в конце речи — малая доля записи: вся запись речевая,
				// и разбор целиком её не признаёт шумом.
				a.sessionLog("segment-fallback", "reason=tail-noise")
				segmentFallback, transcript, finalAudio = true, "", false
				continue
			} else {
				// Ни одного слова: вся запись — шум. Это испорченный вход, а не
				// тишина, и звук ложится в историю строкой failed, чтобы его можно
				// было переотправить.
				path, err := a.compressChunk(chunk)
				if err != nil {
					a.completeSession(session, "error", transcript, err)
					return
				}
				a.keepFailedPhrase(path)
				a.completeSession(session, "error", transcript, errNoisyMicrophone)
				return
			}
		}
		if speech {
			// Отметки времени по всему пути: без них «долговато» неотличимо от
			// «показалось», и причину приходится угадывать.
			a.sessionLog("timing", fmt.Sprintf("chunk-ready ms=%d bytes=%d final=%t", time.Since(a.began).Milliseconds(), len(chunk), final))
			path, err := a.compressChunk(chunk)
			if err != nil {
				a.completeSession(session, "error", transcript, err)
				return
			}
			// В журнал идёт вес посылки: по нему видно, сжатое уходит в разбор
			// или нет, и с чем сравнивать время разбора.
			weight := int64(0)
			if info, statErr := os.Stat(path); statErr == nil {
				weight = info.Size()
			}
			a.sessionLog("timing", fmt.Sprintf("phrase-file ms=%d bytes=%d", time.Since(a.began).Milliseconds(), weight))
			// Хвост предыдущего куска едет подсказкой и хвосту записи: речь
			// связная, и распознаватель не должен начинать её заново уже
			// разобранным началом.
			prompt := ""
			if !segmentFallback {
				prompt = dictsegment.PromptTail(dictsegment.Join(segmentTexts...), dictsegment.PromptLimit)
			}
			text, err := a.recognizeTailWithRetry(session, path, prompt)
			a.sessionLog("timing", fmt.Sprintf("recognized ms=%d words=%d", time.Since(a.began).Milliseconds(), len(strings.Fields(text))))
			if err == nil {
				recognizedChunks++
			}
			if err != nil {
				if session.cancelled.Load() {
					// Человек отменил, пока шёл разбор: повторов уже не будет,
					// и запись, которую он отменил, в историю не идёт.
					_ = os.Remove(path)
					a.completeSession(session, "cancelled", transcript, nil)
					return
				}
				if tookFinal && !segmentFallback && len(segmentTexts) > 0 {
					// Хвост после удачно разобранных кусков не разобрался:
					// прежний путь — вся запись целиком, как было до нарезки.
					// Куски в историю не идут: целая запись покроет и их, а
					// иначе остались бы две строки — половина разговора и
					// отдельный хвост, которые нечем склеить. Отказ целой
					// записи обрабатывается ниже как обычно: строка повтора с
					// ЦЕЛОЙ записью, а не с одним хвостом.
					_ = os.Remove(path)
					a.sessionLog("segment-fallback", "reason=tail-not-recognized")
					segmentFallback, transcript, finalAudio = true, "", false
					continue
				}
				if errors.Is(err, errCloudUnavailable) {
					// Облако не ответило, а местной подмены при выбранном
					// облаке нет. Фраза встаёт в историю со статусом retrying и
					// ждёт автоповтора: служба сама проверит связь через
					// полминуты и разберёт её, когда та вернётся.
					a.keepUnsentPhrase(path, err)
					_, full := cloudTrouble(err)
					a.completeSession(session, "error", transcript, fmt.Errorf("%s Запись сохранена и будет повторена автоматически.", full))
					return
				}
				// Местный разбор (облако не выбрано) тоже не теряет звук:
				// строка встаёт в историю со статусом failed, повтор — по
				// кнопке в окне.
				a.keepFailedPhrase(path)
				a.completeSession(session, "error", transcript, fmt.Errorf("распознавание не удалось. Фрагмент сохранён: %s", path))
				return
			}
			_ = os.Remove(path)
			if session.cancelled.Load() || a.ctx.Err() != nil {
				a.completeSession(session, "cancelled", transcript, nil)
				return
			}
			words := strings.Fields(text)
			limit := len(words)
			if !phraseFinal {
				limit = stableWordCount(previousWords, words)
				// A repeated prefix is stable; keep only the unfinished edge word.
				limit = min(limit, max(0, len(words)-1))
			}
			text = ""
			if limit > committed {
				text = strings.Join(words[committed:limit], " ")
				committed = limit
			}
			previousWords = words
			if phraseFinal {
				previousWords = nil
				committed = 0
			}
			if text != "" {
				if transcript != "" {
					text = " " + text
				}
				transcript += text
				// The disk copy is what survives a crash; it is written as the
				// words arrive rather than when the conversation ends. Нарезка
				// пишет весь текст разом: по кускам он в журнал не попадал
				// нарочно — иначе отменённый разговор всплыл бы в истории
				// половиной текста.
				session.journal.append(transcript)
			}
		}
		// Итог ожидания после отпускания клавиши: столько прошло от release до
		// готовности последнего куска. Без этой строки ускорение неотличимо от
		// «показалось».
		if tookFinal && !releasedAt.IsZero() {
			a.sessionLog("timing", fmt.Sprintf("release-recognized ms=%d", time.Since(releasedAt).Milliseconds()))
			releasedAt = time.Time{}
		}
		a.mu.Lock()
		a.state.Transcript = transcript
		a.mu.Unlock()
		if final {
			autostopped := session.autostop.Load()
			if transcript == "" {
				if noiseSkipped > 0 && recognizedChunks == 0 {
					// Ни один кусок не ушёл в распознавание, а шум был: весь звук
					// — шум или тишина, вход испорчен. Запись уходит в историю
					// строкой error. Это важнее автостопа: поломку входа молчание
					// не должно прятать. Если же речь распозналась, но облако
					// вернуло пусто, — это тишина, а не шум (ниже).
					a.completeSession(session, "error", "", errNoisyMicrophone)
					return
				}
				if autostopped {
					a.finishAutostopEmpty(session)
					return
				}
				a.completeSession(session, "silence", "", nil)
				return
			}
			// Готовый текст целиком в буфер и ровно один Ctrl+V. Ни ожидания
			// поля, ни помощника, ни повторов: помощник поля писал текст по
			// знакам, и из-за этого фраза ложилась в чужое окно второй раз.
			destination := "clipboard"
			var trouble error
			if err := clipboardWrite(transcript); err != nil {
				a.sessionLog("paste", fmt.Sprintf("ok=false reason=clipboard-unavailable %s", err))
				trouble = fmt.Errorf("не удалось обновить буфер обмена: %w", err)
			} else if ok, how := pasteInto(target); ok {
				a.sessionLog("paste", fmt.Sprintf("ok=true %s", how))
				destination = "pasted"
			} else {
				a.sessionLog("paste", fmt.Sprintf("ok=false reason=%s", how))
				trouble = fmt.Errorf("не удалось вставить текст (%s). Полный текст сохранён в буфере обмена", how)
			}
			// Итог уходит на плашку СЕЙЧАС, до записи истории и уборки: пока
			// диск занят, человек уже видит галочку или «В буфере обмена».
			// CAS в setFinalOverlay не даст completeSession повторить его.
			a.setFinalOverlay(destination)
			// Итог автостопа с текстом — обычная запись: строка истории со статусом
			// done. Тишину видно в журнале разговора: recording-end stage=autostop.
			stage := destination
			if autostopped {
				stage = "autostop"
			}
			a.mu.Lock()
			a.state.Destination = destination
			if trouble != nil {
				a.state.Trouble = trouble.Error()
			}
			stored := a.addHistoryLocked(historyEntry(transcript, a.state.Recording, destination,
				a.state.Seconds, statusDone, "", false), true)
			if stored {
				a.touchHistoryLocked()
			}
			a.mu.Unlock()
			// Запись истории — вне замка: снимок снят, а диск не держит окно.
			// Журнал метится ПОСЛЕ неё (completeSession): иначе сбой между двумя
			// записями потерял бы разговор, как это вышло с 20261111-231748.
			if stored {
				session.historySaved = a.saveHistory() == nil
			} else {
				// Строка уже была: история этот разговор знает.
				session.historySaved = true
			}
			a.sessionLog("recording-finish", fmt.Sprintf("words=%d destination=%s", len(strings.Fields(transcript)), destination))
			a.completeSession(session, stage, transcript, trouble)
			// Всплывающей подсказки здесь больше нет. Она рисовалась системным
			// окном STATIC — серым прямоугольником поверх чужих окон, — и
			// выглядела как мусор на экране. Молчание лучше: текст в буфере
			// человек увидит там, куда вставляет.
			return
		}
		select {
		case <-stop:
			final = true
			stop = nil
			// Отметка отпускания: от неё до готовности последнего куска видно,
			// сколько человек ждёт после того, как перестал говорить.
			a.mu.Lock()
			seconds := a.state.Seconds
			a.mu.Unlock()
			releasedAt = a.began.Add(time.Duration(seconds * float64(time.Second)))
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func stableWordCount(previous, current []string) int {
	count := 0
	for count < len(previous) && count < len(current) {
		left := strings.Trim(previous[count], ".,!?;:")
		right := strings.Trim(current[count], ".,!?;:")
		if !strings.EqualFold(left, right) {
			break
		}
		count++
	}
	return count
}

func (a *App) completeSession(session *recordingSession, stage, text string, err error) {
	// Чем кончился разговор, видно в журнале всегда, а не только при удаче.
	// Разговор, оборвавшийся через пять секунд на чужой машине, до этой строки
	// не оставлял в журнале ничего: оставалось гадать, замолчал микрофон,
	// отказало распознавание или человек нажал сочетание второй раз.
	a.sessionLog("recording-end", fmt.Sprintf("stage=%s words=%d trouble=%t", stage, len(strings.Fields(text)), err != nil))
	// Обычный путь итога — остальные исходы (ошибка, тишина, отмена).
	// В успешном пути плашку уже обновила вставка, и CAS ничего не повторит.
	a.setFinalOverlay(stage)
	a.mu.Lock()
	if a.session == session && a.state.Stage == "recording" {
		_ = a.audio.Stop()
		// Закрывает канал ровно один раз: ту же остановку может прислать и
		// выключение Windows (см. stop.go).
		session.end()
	}
	a.mu.Unlock()
	if stage == "error" {
		// Отказ распознавания не должен оставлять человека без записи: звук уже
		// на диске, и в истории появляется строка со статусом error и причиной.
		// История пишется ПЕРВОЙ, метка в журнале — в complete: иначе сбой между
		// ними потерял бы запись.
		session.historySaved = a.rememberFailure(stage, text, err)
	}
	a.completeWithSession(session, stage, text, err)
	if stage == "error" {
		return
	}
	// Прерванный выключением разговор не разбирается: на выключение у программы
	// секунды, а распознавание идёт дольше. Запись уже сжата (Ogg/Opus) и
	// останется на диске — её разберут при следующем запуске.
	if stage == "silence" || stage == "cancelled" || stage == "interrupted" {
		return
	}
	// Сжимать больше нечего: запись велась сразу в Ogg/Opus, и на диске лежит
	// та самая сжатая запись, которую человек услышит. Прежний второй заход
	// (WAV → AAC помощником Windows) убран вместе с WAV.
}

// stopForVoiceIdle — автостоп по тишине. Ведёт себя как отпускание клавиши
// (Finish): звук останавливается, стадия — processing, плашка — processing, и
// длительность записи выставлена по часам сессии. Поэтому финальный разбор идёт
// при уже остановленном микрофоне, как и при отпускании, а строки истории
// получают настоящую длительность, а не ноль. Если человек отпустил клавишу
// раньше, его Finish уже всё сделал: здесь ничего не трогаем.
func (a *App) stopForVoiceIdle(session *recordingSession, idle time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session != session || a.state.Stage != "recording" {
		return
	}
	seconds := a.timeNow().Sub(session.started).Seconds()
	a.sessionLog("voice-idle-stop", fmt.Sprintf("idle_ms=%d seconds=%.1f", idle.Milliseconds(), seconds))
	_ = a.audio.Stop()
	a.setOverlay(OverlayState{Visible: true, Stage: "processing", Seconds: seconds})
	a.state.Seconds = seconds
	a.state.Stage = "processing"
	session.autostop.Store(true)
	session.end()
}

// finishAutostopEmpty — автостоп без единого слова. Строка истории заводится со
// статусом autostop, без текста и без пути к звуку (звук не нужен: слов нет,
// как и при тишине). Ошибки здесь нет, поэтому rememberFailure не зовётся.
func (a *App) finishAutostopEmpty(session *recordingSession) {
	a.mu.Lock()
	entry := historyEntry("", "", "autostop", a.state.Seconds, statusAutostop, autostopReason, false)
	stored := a.addHistoryLocked(entry, true)
	if stored {
		a.touchHistoryLocked()
	}
	a.mu.Unlock()
	// Как и в обычном итоге: журнал метится после записи истории.
	if stored {
		session.historySaved = a.saveHistory() == nil
	} else {
		session.historySaved = true
	}
	a.completeSession(session, "autostop", "", nil)
}

// rememberFailure кладёт в историю запись о неудачном разговоре: звук цел,
// причина названа, и человек может попробовать расшифровать заново. Возвращает,
// легла ли строка в history.json: метку в журнале без этого ставить нельзя.
func (a *App) rememberFailure(stage, text string, cause error) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	audio := a.state.Recording
	if audio == "" {
		return false
	}
	reason := failureReason(cause)
	if _, err := os.Stat(audio); err != nil {
		// Звук пропал или испорчен — это не отказ облака, и повтор его не
		// вылечит: человеку надо знать, что запись уже не спасти.
		reason = "файл записи повреждён или пропал"
	}
	entry := historyEntry(text, audio, stage, a.state.Seconds, statusError, reason, false)
	if !a.addHistoryLocked(entry, true) {
		// Строка уже была: история этот разговор знает.
		return true
	}
	if err := a.saveHistoryLocked(); err != nil {
		a.sessionLog("history-write-failed", err.Error())
		return false
	}
	return true
}
