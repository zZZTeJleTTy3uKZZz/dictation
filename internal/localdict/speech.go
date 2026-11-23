package localdict

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/opusrec"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
)

// Разговор, который ведёт чужая программа: звук приходит кусками по сети, слова
// уходят обратно в конце. Это не наша диктовка — у той звук берёт микрофон, а
// слова кладёт помощник прямо в поле, — но разбор тот же: тот же облачный способ
// и тот же словарь терминов. Отличие ровно в двух концах: звук приносит кто-то
// другой и слова забирает тоже он.
//
// ВАЖНО: разбор только облачный. Прежде куски разбирались местной моделью, и это
// был единственный путь, умевший отвечать по ходу речи; местных моделей в
// диктовке больше нет (решение владельца), поэтому разговор идёт записью целиком
// — её облако разбирает в конце, как и обычную диктовку.
const (
	speechRate = 16000
	// Разговор, за которым перестали приходить, не держит разбор вечно.
	speechIdle     = 2 * time.Minute
	speechSessions = 4
	// Куски приходят по двести миллисекунд; мегабайт — это тридцать секунд,
	// то есть с запасом на то, что кто-то пришлёт скопом.
	maxSpeechBytes = 1 << 20
)

type speechSession struct {
	id      string
	started time.Time
	touched time.Time
	// cloud — облачный способ, которым разбирается этот разговор.
	cloud speech.Channel
	// savedPath и savedSeconds — запись разговора, уже закрытая облачным
	// разбором, и её длина. finishSpeech забирает их отсюда: закрывать второй
	// раз нельзя, а длину после закрытия уже не спросить.
	savedPath    string
	savedSeconds float64

	mu         sync.Mutex
	transcript string

	// Запись разговора ведётся с первого куска: разбор может не получиться,
	// человек может закрыть окно, а звук остаётся единственным, что ещё можно
	// послушать. Пишется сразу Ogg/Opus, страница — раз в секунду: если
	// процесс убьют, файл всё равно открывается до последней целой страницы.
	recorder *opusrec.Recorder
}

// writeAudio отдаёт звук в запись разговора. Ошибка записи — не повод ронять
// разбор: звук мог не лечь на диск, но сказанное ещё можно распознать.
func (s *speechSession) writeAudio(pcm []byte) {
	if s.recorder == nil {
		return
	}
	if err := s.recorder.Write(pcm); err != nil {
		logLine(filepath.Dir(s.recorder.Path()), "recording-write-failed",
			"audio="+filepath.Base(s.recorder.Path())+" err="+err.Error())
		s.recorder.Abort()
		s.recorder = nil
	}
}

func speechID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// SpeechStart открывает разговор и отвечает его именем. Способ берётся выбранный
// в настройках: чем человек диктует, тем и чужая программа слышит.
//
// Своей модели у облака нет вовсе, и требовать её было бы нечем: нет готового
// способа — это отказ с причиной, а не повод взять что-то ещё.
func (a *App) SpeechStart() (string, speech.Channel, error) {
	cloud := a.networkChannel()
	if cloud == nil {
		return "", nil, cloudRefusal(nil)
	}
	id, err := speechID()
	if err != nil {
		return "", cloud, err
	}
	session := &speechSession{id: id, started: time.Now(), touched: time.Now(), cloud: cloud}
	session.recorder = a.openSpeechAudio(id)
	a.speechMu.Lock()
	defer a.speechMu.Unlock()
	if a.speech == nil {
		a.speech = map[string]*speechSession{}
	}
	a.expireSpeechLocked()
	if len(a.speech) >= speechSessions {
		return "", cloud, fmt.Errorf("слишком много разговоров сразу: %d", len(a.speech))
	}
	a.speech[id] = session
	return id, cloud, nil
}

// expireSpeechLocked убирает разговоры, за которыми перестали приходить.
func (a *App) expireSpeechLocked() {
	for id, session := range a.speech {
		session.mu.Lock()
		idle := time.Since(session.touched) > speechIdle
		text := session.transcript
		session.mu.Unlock()
		if !idle {
			continue
		}
		delete(a.speech, id)
		// За брошенным разговором уже никто не придёт, но сказанное в нём
		// остаётся. Запись закрывается в стороне: это секунды, и открывать
		// новый разговор они ждать не должны.
		go a.finishSpeech(session, text)
	}
}

// openSpeechAudio открывает запись разговора. Имя начинается с даты, как у
// остальных записей: по нему же решается, какие записи старые. Пишется сразу
// Ogg/Opus — сжатый звук и есть та запись, которую человек услышит; второго
// прохода упаковки после разговора больше нет.
func (a *App) openSpeechAudio(id string) *opusrec.Recorder {
	dir := filepath.Join(a.Media, "recordings")
	if os.MkdirAll(dir, 0700) != nil {
		return nil
	}
	stamp := time.Now().Format("20060102-150405")
	path := filepath.Join(dir, fmt.Sprintf("%s-%s-speech.ogg", stamp, id[:6]))
	recorder, err := opusrec.Start(path)
	if err != nil {
		logLine(dir, "recording-file-unavailable", "speech="+filepath.Base(path)+" err="+err.Error())
		return nil
	}
	return recorder
}

// closeSpeechAudio закрывает запись и отдаёт её путь с длительностью. Пустой
// путь означает, что сохранять нечего.
func (a *App) closeSpeechAudio(session *speechSession) (string, float64) {
	recorder := session.recorder
	session.recorder = nil
	if recorder == nil {
		return "", 0
	}
	seconds := recorder.Seconds()
	path := recorder.Close()
	// Полсекунды — это промах по клавише, а не разговор: в истории он был бы
	// мусором и занимал бы место наравне с настоящей диктовкой.
	if path == "" || seconds < .5 {
		if path != "" {
			_ = os.Remove(path)
		}
		return "", 0
	}
	return path, seconds
}

// finishSpeech кладёт запись разговора в общую историю. Текст может быть и
// пустым: если разбор отказал, звук всё равно остаётся — текст человек наберёт
// заново, а голос уже не вернуть.
func (a *App) finishSpeech(session *speechSession, text string) {
	session.mu.Lock()
	path, seconds := a.closeSpeechAudio(session)
	if path == "" {
		// Запись уже закрыта облачным разбором: её и кладём в историю.
		path, session.savedPath = session.savedPath, ""
		seconds, session.savedSeconds = session.savedSeconds, 0
	}
	session.mu.Unlock()
	if path == "" {
		return
	}
	pruneRecordings(filepath.Dir(path), recordingLimit)
	a.rememberPlugged(strings.TrimSpace(text), path, seconds)
}

func (a *App) speechSession(id string) (*speechSession, error) {
	a.speechMu.Lock()
	session, ok := a.speech[id]
	a.speechMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("разговор %q не найден: он закончен или не начинался", id)
	}
	return session, nil
}

// SpeechFeed принимает очередной кусок звука и отвечает всем, что уже
// распознано. Ответ — весь текст разговора, а не прибавка: чужая программа сама
// решает, чем заменить показанное, и лишнего не приписывает.
func (a *App) SpeechFeed(ctx context.Context, id string, pcm []byte, pause bool) (string, error) {
	session, err := a.speechSession(id)
	if err != nil {
		return "", err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.touched = time.Now()
	if len(pcm) != 0 {
		// Облако разбирает запись целиком и только в конце: копить звук в
		// памяти незачем — он уже пишется в запись разговора, и оттуда же
		// уйдёт в запрос. Ответ пустой: чужой программе нечего показывать
		// до конца разговора, и она оставляет прежний текст.
		session.writeAudio(pcm)
	}
	return session.transcript, nil
}

// SpeechStop заканчивает разговор: запись закрывается и уходит в облако, текст
// отдаётся целиком. Разговор после этого не живёт — за ним никто не придёт.
func (a *App) SpeechStop(ctx context.Context, id string) (string, float64, error) {
	session, err := a.speechSession(id)
	if err != nil {
		return "", 0, err
	}
	session.mu.Lock()
	// Разговор без звука — это промах по клавише, а не диктовка: закрывать
	// нечего и разбирать нечего, и отказом это не считается.
	// Разговор без звука — это промах по клавише, а не диктовка: закрывать и
	// разбирать нечего, и отказом это не считается. Секунды берутся у записи:
	// короткая запись так же ничего не значит.
	hasAudio := session.savedPath != ""
	if session.recorder != nil && session.recorder.Seconds() >= .5 {
		hasAudio = true
	}
	if hasAudio {
		err = a.cloudSpeech(ctx, session)
	}
	session.mu.Unlock()
	// Разговор уходит из списка до записи в историю: пока сжатие считает свои
	// секунды, имя уже свободно, и никто не примет его за живой разговор.
	a.speechMu.Lock()
	delete(a.speech, id)
	a.speechMu.Unlock()
	session.mu.Lock()
	text := session.transcript
	seconds := session.savedSeconds
	if session.recorder != nil {
		seconds = session.recorder.Seconds()
	}
	session.mu.Unlock()
	// Длина разговора берётся по записанному звуку, а не по сумме разобранных
	// фраз: замолчанные куски в неё не попадали, и время выходило короче.
	a.finishSpeech(session, text)
	if err != nil && text == "" {
		return "", seconds, err
	}
	return text, seconds, nil
}
