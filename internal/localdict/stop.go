package localdict

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Остановка диктовки — это не просьба оборвать разговор.
//
// Случай 11.11.2026: установка обновления остановила службу посреди диктовки
// владельца, и запись оборвалась. Программа не знала, что её останавливают
// ровно в тот момент, когда человек говорит, и уходила, не закрыв сессию.
//
// Отсюда правило, общее для всех путей остановки — установщика, скрипта
// выкатки, службы Windows и выключения машины:
//
//   - идёт сессия (запись или разбор после «стоп») — её доводят до конца и
//     только потом уходят, ожидая не дольше предела из настроек;
//   - не дождались — отказывают в остановке словами и кодом возврата, но
//     программу не убивают: пусть откажется установка, а не разговор;
//   - выключение Windows — не ожидание, а закрытие: звук дописывается на диск,
//     файл и журнал закрываются, разбор не ждётся.
const (
	// DefaultStopWait — сколько диктовка ждёт конца записи, прежде чем отказать
	// в остановке. Десять минут: обновление не должно откладываться на часы, а
	// фраза не должна обрываться на полуслове.
	DefaultStopWait = 10 * time.Minute
	// MinStopWait и MaxStopWait — пределы настройки: меньше полуминуты не даёт
	// закрыть даже короткую фразу, больше часа превращает установку в ожидание.
	MinStopWait = 30 * time.Second
	MaxStopWait = 60 * time.Minute

	// ExitStopped — диктовки нет или она ушла: остановка удалась.
	ExitStopped = 0
	// ExitFailed — ответа не было или он не разобран. Это не «идёт запись», а
	// поломка связи, и установщик должен назвать её, а не ставить вслепую.
	ExitFailed = 1
	// ExitRecording — идёт запись. Этим кодом отвечают и --status («да, идёт»),
	// и --quit («не дождались, остановка отложена»): для установщика и скрипта
	// выкатки это одно и то же — трогать файлы нельзя.
	ExitRecording = 3

	// quitExitGrace — сколько ждать ухода программы после её согласия. Сессия к
	// этому времени уже закрыта; остаётся дописать историю и убрать файл адреса.
	quitExitGrace = 60 * time.Second
	// shutdownFlushWait — сколько ждать закрытия записи при выключении Windows.
	// Windows даёт программе секунды, а не минуты: звук закрывается сразу, а
	// разбор не ждётся вовсе.
	shutdownFlushWait = 3 * time.Second
)

// StopStatus — состояние записи, о котором спрашивают установщик, скрипт
// выкатки и служба: идёт ли сессия и с какого времени.
type StopStatus struct {
	Recording bool   `json:"recording"`
	Since     string `json:"since,omitempty"`
	Stage     string `json:"stage,omitempty"`
	Seconds   int64  `json:"seconds,omitempty"`
	// WaitSeconds — сколько диктовка готова ждать конца записи. Установщику это
	// число нужно, чтобы не ждать дольше самой программы.
	WaitSeconds int `json:"wait_seconds"`
	// Reason — почему остановка не удалась, словами для человека.
	Reason string `json:"reason,omitempty"`
}

// StopOptions — как просить диктовку выйти.
type StopOptions struct {
	// Wait — сколько ждать конца записи; ноль — предел из настроек диктовки.
	Wait time.Duration
	// Flush — выключение Windows: закрыть запись и выйти, не дожидаясь разбора.
	Flush bool
}

// StopRefused — диктовка не остановлена: идёт запись, и ждать её конца больше
// нельзя. Для установщика это отказ установки, а не повод убить программу.
type StopRefused struct {
	Since  time.Time
	Waited time.Duration
	Reason string
}

func (e *StopRefused) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	waited := e.Waited.Round(time.Second)
	if e.Since.IsZero() {
		return fmt.Sprintf("идёт запись, остановка отложена: ждали %s, сессия не кончилась", waited)
	}
	return fmt.Sprintf("идёт запись с %s, остановка отложена: ждали %s, сессия не кончилась",
		e.Since.Format("15:04:05"), waited)
}

// ParseStopOptions разбирает ключи, с которыми диктовку просят выйти:
//
//	--quit              — ждать конца записи не дольше предела из настроек
//	--quit --wait 120   — ждать не дольше 120 секунд
//	--quit --flush      — выключение Windows: закрыть запись и выйти сразу
//
// Незнакомый ключ — отказ, а не молчаливое «показалось»: опечатка в установщике
// иначе превратилась бы в «ждали ноль секунд» и в оборванную запись.
func ParseStopOptions(args []string) (StopOptions, error) {
	options := StopOptions{}
	for at := 0; at < len(args); at++ {
		switch args[at] {
		case "--wait":
			if at+1 >= len(args) {
				return StopOptions{}, errors.New("после --wait нужно число секунд")
			}
			seconds, err := strconv.Atoi(args[at+1])
			if err != nil || seconds < 0 {
				return StopOptions{}, fmt.Errorf("не понять, сколько ждать: %q", args[at+1])
			}
			options.Wait = time.Duration(seconds) * time.Second
			if options.Wait > MaxStopWait {
				options.Wait = MaxStopWait
			}
			at++
		case "--flush":
			options.Flush = true
		default:
			return StopOptions{}, fmt.Errorf("неизвестный ключ: %q", args[at])
		}
	}
	return options, nil
}

// sessionActiveStage — идёт ли сессия диктовки: запись или разбор после «стоп».
// Правило одно на всю программу: пока сессия не закрыта, звук, файл записи и
// журнал принадлежат ей, и обрывать их нельзя.
func sessionActiveStage(stage string) bool {
	return stage == "recording" || stage == "processing"
}

// SessionActive — идёт ли сессия диктовки прямо сейчас.
func (a *App) SessionActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return sessionActiveStage(a.state.Stage)
}

// SessionSince — когда началась идущая сессия; ноль — сессии нет.
func (a *App) SessionSince() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !sessionActiveStage(a.state.Stage) {
		return time.Time{}
	}
	return a.began
}

// stopWaitDuration переводит настройку в срок ожидания. Ноль и значение вне
// пределов дают умолчание: испорченный файл настроек не должен превращаться в
// «не ждать вовсе» — по правилу выше такой файл работу не выключает.
func stopWaitDuration(seconds int) time.Duration {
	wait := time.Duration(seconds) * time.Second
	if wait < MinStopWait || wait > MaxStopWait {
		return DefaultStopWait
	}
	return wait
}

// StopWaitSetting — предел ожидания из настроек диктовки. Один и тот же ответ
// нужен самой программе, службе Windows и установщику: второго мнения о том,
// сколько ждать, в этом хозяйстве быть не должно.
func StopWaitSetting(dir string) time.Duration {
	var config Config
	if data, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		_ = json.Unmarshal(data, &config)
	}
	return stopWaitDuration(config.StopWaitSeconds)
}

// stopStatus собирает ответ о сессии для /status и для отказа.
func (a *App) stopStatus() StopStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	status := StopStatus{WaitSeconds: int(stopWaitDuration(a.state.Config.StopWaitSeconds) / time.Second)}
	if !sessionActiveStage(a.state.Stage) {
		return status
	}
	status.Recording = true
	status.Stage = a.state.Stage
	if !a.began.IsZero() {
		status.Since = a.began.Format(time.RFC3339)
		status.Seconds = int64(time.Since(a.began).Seconds())
	}
	return status
}

// waitForSession ждёт конца идущей сессии не дольше wait. Возвращает false, если
// сессия так и не кончилась.
func (a *App) waitForSession(wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for a.SessionActive() {
		if wait <= 0 || !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-a.ctx.Done():
			// Программу уже останавливают с другой стороны: второй разговор об
			// этом не нужен.
			return true
		case <-time.After(200 * time.Millisecond):
		}
	}
	return true
}

// FlushForShutdown закрывает идущую сессию так, как это делает выключение
// Windows: звук дописан на диск, страница конца и файл закрыты, а разбор не
// ждётся. Журнал сессии остаётся незаконченным нарочно — по нему следующий
// запуск восстановит запись и разберёт её, когда машина поднимется.
//
// Возвращает файл, в который легла запись; пусто — сессии не было.
func (a *App) FlushForShutdown() string {
	a.mu.Lock()
	stage := a.state.Stage
	if !sessionActiveStage(stage) {
		a.mu.Unlock()
		return ""
	}
	session := a.session
	recording := stage == "recording"
	path := a.state.Recording
	if recording {
		// Устройство останавливаем здесь же: после этого в файл ничего не
		// дописывается, и его можно закрывать.
		_ = a.audio.Stop()
		a.state.Seconds = time.Since(a.began).Seconds()
	}
	a.mu.Unlock()
	if session != nil {
		// Прерывание ставится ДО закрытия канала: нить сессии увидит его первой
		// строкой своего круга, закроет звук и уйдёт, не заходя в облако. Это не
		// отмена человеком: запись остаётся на диске и разберётся при следующем
		// запуске. Журнал закрывает та же нить (complete со стадией interrupted):
		// закрывать его отсюда — значит трогать чужой файл мимо замка сессии.
		session.interrupted.Store(true)
		if recording {
			session.end()
		}
	}
	deadline := time.Now().Add(shutdownFlushWait)
	for a.SessionActive() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	// Нить могла не успеть — тогда файл закрывается здесь же. Заголовок волны
	// дописывается, и на диске остаётся читаемая запись, а не обрывок.
	if closed := a.audio.CloseRecording(); closed != "" {
		a.mu.Lock()
		a.state.Recording, a.state.Stage = closed, "interrupted"
		// Даже если нить сессии не успела дойти до complete, фраза кончилась:
		// её канал не должен достаться повтору и API.
		a.phraseChannel = nil
		a.busy = false
		a.idleSince = time.Now()
		a.mu.Unlock()
		path = closed
	}
	a.sessionLog("session-flushed", fmt.Sprintf("was=%s audio=%s", stage, filepath.Base(path)))
	return path
}

// stopRoute — GET /status и POST /quit для установщика, скрипта выкатки, службы
// и выключения Windows.
func (a *App) stopRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		writeStopStatus(w, a.stopStatus(), http.StatusOK)
	})
	mux.HandleFunc("POST /quit", a.quitRoute)
}

// quitRoute — просьба остановиться. Пока идёт сессия, она ждёт её конца: не
// дождалась — отвечает 409 и остаётся работать.
func (a *App) quitRoute(w http.ResponseWriter, r *http.Request) {
	// Ожидание бывает минутами, а обычный предел ответа в десять секунд оборвал
	// бы соединение раньше ответа.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	wait := a.stopWait()
	if raw := strings.TrimSpace(r.URL.Query().Get("wait")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 0 {
			http.Error(w, "не понять, сколько ждать: "+raw, http.StatusBadRequest)
			return
		}
		wait = time.Duration(seconds) * time.Second
		if wait > MaxStopWait {
			wait = MaxStopWait
		}
	}
	if r.URL.Query().Get("flush") == "1" {
		// Выключение Windows: закрываем запись и уходим, не ожидая разбора.
		a.FlushForShutdown()
		a.sessionLog("stop-flush", "reason=shutdown")
		writeStopStatus(w, a.stopStatus(), http.StatusOK)
		flushAndCancel(a, w)
		return
	}
	since := a.SessionSince()
	if since.IsZero() {
		a.sessionLog("stop-now", "reason=no-session")
		writeStopStatus(w, a.stopStatus(), http.StatusOK)
		flushAndCancel(a, w)
		return
	}
	a.sessionLog("stop-deferred", fmt.Sprintf("since=%s wait=%s", since.Format(time.RFC3339), wait))
	if a.waitForSession(wait) {
		a.sessionLog("stop-deferred-done", fmt.Sprintf("waited_ms=%d", time.Since(since).Milliseconds()))
		writeStopStatus(w, a.stopStatus(), http.StatusOK)
		flushAndCancel(a, w)
		return
	}
	status := a.stopStatus()
	status.Reason = fmt.Sprintf("идёт запись с %s: остановка отложена на %s и не дождалась конца. "+
		"Запись не прервана, диктовка продолжает работать — попробуйте позже.",
		since.Format("15:04:05"), wait)
	a.sessionLog("stop-refused", fmt.Sprintf("since=%s wait=%s", since.Format(time.RFC3339), wait))
	writeStopStatus(w, status, http.StatusConflict)
}

// flushAndCancel отдаёт ответ на провод и только потом гасит программу: иначе
// закрытое соединение выглядит у установщика как «диктовка не ответила».
func flushAndCancel(a *App, w http.ResponseWriter) {
	_ = http.NewResponseController(w).Flush()
	a.cancel()
}

// stopWait — предел ожидания этой диктовки из её настроек.
func (a *App) stopWait() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return stopWaitDuration(a.state.Config.StopWaitSeconds)
}

func writeStopStatus(w http.ResponseWriter, status StopStatus, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(status)
}

// readStopEndpoint читает адрес работающей диктовки. Отсутствие файла — «никто не
// работает», а не ошибка; испорченный файл — ошибка: молча «остановить»
// несуществующее значит оставить работающую программу и провалить установку
// позже.
func readStopEndpoint(media string) (Endpoint, bool, error) {
	data, err := os.ReadFile(filepath.Join(media, "endpoint.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Endpoint{}, false, nil
		}
		return Endpoint{}, false, err
	}
	var endpoint Endpoint
	if json.Unmarshal(data, &endpoint) != nil || endpoint.URL == "" || endpoint.PID == 0 {
		return Endpoint{}, false, errors.New("адрес работающей диктовки не прочитан")
	}
	return endpoint, true, nil
}

// StatusRunning спрашивает работающую диктовку, идёт ли запись. Никто не
// работает — запись не идёт: для установщика это ответ, а не поломка.
func StatusRunning(ctx context.Context, media string) (StopStatus, error) {
	endpoint, found, err := readStopEndpoint(media)
	if err != nil {
		return StopStatus{}, err
	}
	if !found {
		return StopStatus{Reason: "диктовка не запущена"}, nil
	}
	request, err := http.NewRequestWithContext(ctx, "GET", endpoint.URL+"/status", nil)
	if err != nil {
		return StopStatus{}, err
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return StopStatus{}, fmt.Errorf("диктовка не отвечает: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		// Прежняя сборка ключа /status ещё не знает, но отдаёт стадию в /state.
		// Обновление с неё начинается именно так, и без этого запасного хода
		// установщик не смог бы отличить «идёт запись» от «не знаю».
		return statusFromState(ctx, endpoint)
	}
	if response.StatusCode != http.StatusOK {
		return StopStatus{}, fmt.Errorf("диктовка не сказала о записи: %s", response.Status)
	}
	var status StopStatus
	if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&status) != nil {
		return StopStatus{}, errors.New("ответ диктовки о записи не разобран")
	}
	return status, nil
}

// QuitRunning просит работающую диктовку выйти и ждёт, пока она уйдёт.
//
// Установщик не может заменить файлы программы, пока она запущена: Windows не
// даёт перезаписать работающий exe. Поэтому демона просят выйти по-хорошему, а
// он сперва доводит до конца идущую сессию — иначе обновление обрывало бы
// разговор, который человек ведёт прямо сейчас.
//
// Возвращает nil и когда никто не работает: для установщика это тот же успех.
// *StopRefused — сессия не кончилась за отведённое время: установка
// откладывается, программа продолжает работать.
func QuitRunning(ctx context.Context, media string, options StopOptions) error {
	endpoint, found, err := readStopEndpoint(media)
	if err != nil || !found {
		return err
	}
	wait := options.Wait
	if wait == 0 && !options.Flush {
		// Предел берём у самой диктовки: он настраивается, и второй догадки о
		// том, сколько ждать, у установщика быть не должно.
		if status, statusErr := StatusRunning(ctx, media); statusErr == nil {
			wait = time.Duration(status.WaitSeconds) * time.Second
		}
		if wait <= 0 {
			wait = DefaultStopWait
		}
	}
	query := url.Values{}
	if options.Wait > 0 {
		query.Set("wait", strconv.Itoa(int(options.Wait/time.Second)))
	}
	if options.Flush {
		query.Set("flush", "1")
	}
	address := endpoint.URL + "/quit"
	if len(query) > 0 {
		address += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, "POST", address, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("диктовка не отвечает: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusConflict:
		var status StopStatus
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&status)
		since, _ := time.Parse(time.RFC3339, status.Since)
		return &StopRefused{Since: since, Waited: wait, Reason: status.Reason}
	default:
		return fmt.Errorf("диктовка отказалась выходить: %s", response.Status)
	}
	// Ждём, пока программа действительно уйдёт: установщик заменяет файлы сразу
	// после этого, и «попросили» без «ушла» для него бесполезно.
	deadline := time.Now().Add(quitExitGrace)
	for time.Now().Before(deadline) {
		if !daemonHealthy(ctx, media, endpoint.PID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("диктовка не завершилась за %s", quitExitGrace)
}

// statusFromState — состояние записи по кадру /state: так отвечает сборка, у
// которой ключа /status ещё нет. Длительность сессии там есть, поэтому начало
// записи считается от неё.
func statusFromState(ctx context.Context, endpoint Endpoint) (StopStatus, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", endpoint.URL+"/state?lite=1", nil)
	if err != nil {
		return StopStatus{}, err
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return StopStatus{}, fmt.Errorf("диктовка не отвечает: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return StopStatus{}, fmt.Errorf("диктовка не сказала о записи: %s", response.Status)
	}
	var old struct {
		Stage   string  `json:"stage"`
		Seconds float64 `json:"seconds"`
	}
	// Прежняя сборка на ?lite=1 отвечает полным кадром: замер на работающей
	// 1.11.0 — 615 125 байт (lite-режим появился позже). Предел в 64 КБ на этом
	// кадре обрывал разбор, и обновление с прежней версии не могло отличить
	// «идёт запись» от «не знаю». Берём с запасом, но всё же конечный.
	const maxOldStateBytes = 4 << 20
	if json.NewDecoder(io.LimitReader(response.Body, maxOldStateBytes)).Decode(&old) != nil {
		return StopStatus{}, errors.New("ответ диктовки о записи не разобран")
	}
	status := StopStatus{WaitSeconds: int(DefaultStopWait / time.Second)}
	if !sessionActiveStage(old.Stage) {
		return status, nil
	}
	status.Recording = true
	status.Stage = old.Stage
	status.Seconds = int64(old.Seconds)
	status.Since = time.Now().Add(-time.Duration(old.Seconds * float64(time.Second))).Format(time.RFC3339)
	return status, nil
}

// FormatStatus — ответ --status человеку, а не разборщику JSON.
func FormatStatus(status StopStatus) string {
	if !status.Recording {
		return "диктовка не записывает"
	}
	return fmt.Sprintf("идёт запись с %s (сессия %s, предел ожидания %d с)",
		status.Since, status.Stage, status.WaitSeconds)
}
