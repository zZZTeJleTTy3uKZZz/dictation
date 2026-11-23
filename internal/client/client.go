// Package client — разговор с сервисом: отдать запись, отметиться, слушать события.
//
// Порт meetdigest_watcher/client.py и части main.py. Ровно два дела: найти
// запись и отдать её сервису по ключу — поэтому здесь нет ничего, кроме них.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// uploadTimeout — сколько ждём отправки записи.
	//
	// Час записи в mp3 — это десятки мегабайт, и на слабой сети они едут
	// минутами. Полчаса с запасом; меньше — и отправка рвалась бы на ровном месте.
	uploadTimeout = 30 * time.Minute
	// askTimeout — обычный запрос: отметка, список встреч.
	askTimeout = 30 * time.Second
	// chunkSize — каким куском везём файл при докачке.
	//
	// Восемь мегабайт: достаточно крупно, чтобы не платить за каждый запрос, и
	// достаточно мелко, чтобы обрыв не отбрасывал далеко назад.
	chunkSize = 8 << 20
	// inlineLimit — до какого размера отправляем запись целиком.
	inlineLimit = 24 << 20
)

// Client — сервис глазами рабочей машины.
type Client struct {
	base   string
	key    string
	http   *http.Client
	upload *http.Client
}

func New(base, key string) *Client {
	return &Client{
		base:   strings.TrimRight(base, "/"),
		key:    key,
		http:   &http.Client{Timeout: askTimeout},
		upload: &http.Client{Timeout: uploadTimeout},
	}
}

// Job — задание, каким его видит воркер.
type Job struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Title  string `json:"title"`
}

// SendRecording — отдать запись сервису.
//
// Мелкую — одним запросом, крупную — частями. Порог не про красоту: запрос на
// сотню мегабайт рвётся на слабой сети целиком, и начинать пришлось бы с нуля.
func (c *Client) SendRecording(ctx context.Context, path, title, notes string) (*Job, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("прочитать запись: %w", err)
	}
	if info.Size() > inlineLimit {
		return c.sendInChunks(ctx, path, title, notes, info.Size())
	}
	return c.sendWhole(ctx, path, title, notes)
}

func (c *Client) sendWhole(ctx context.Context, path, title, notes string) (*Job, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("открыть запись: %w", err)
	}
	defer func() { _ = file.Close() }()

	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)
	part, err := form.CreateFormFile("files", filepath.Base(path))
	if err != nil {
		return nil, fmt.Errorf("собрать запрос: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, fmt.Errorf("прочитать запись: %w", err)
	}
	_ = form.WriteField("title", title)
	_ = form.WriteField("source", "watcher")
	if notes != "" {
		_ = form.WriteField("notes", notes)
	}
	if err := form.Close(); err != nil {
		return nil, fmt.Errorf("закрыть запрос: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/jobs", body)
	if err != nil {
		return nil, fmt.Errorf("собрать запрос: %w", err)
	}
	request.Header.Set("X-API-Key", c.key)
	request.Header.Set("Content-Type", form.FormDataContentType())

	var job Job
	if err := c.send(c.upload, request, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// sendInChunks — крупная запись по частям, с продолжением после обрыва.
func (c *Client) sendInChunks(ctx context.Context, path, title, notes string, size int64) (*Job, error) {
	started := map[string]any{
		"filename":    filepath.Base(path),
		"mime":        "audio/mpeg",
		"total_bytes": size,
		"source":      "watcher",
	}
	if title != "" {
		started["title"] = title
	}
	if notes != "" {
		started["notes"] = notes
	}
	var upload struct {
		ID            string `json:"id"`
		ReceivedBytes int64  `json:"received_bytes"`
	}
	if err := c.ask(ctx, http.MethodPost, "/v1/uploads/start", started, &upload); err != nil {
		return nil, fmt.Errorf("начать докачку: %w", err)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("открыть запись: %w", err)
	}
	defer func() { _ = file.Close() }()

	offset := upload.ReceivedBytes
	buffer := make([]byte, chunkSize)
	for offset < size {
		read, err := file.ReadAt(buffer, offset)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("прочитать кусок: %w", err)
		}
		if read == 0 {
			break
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPut,
			c.base+"/v1/uploads/"+upload.ID, bytes.NewReader(buffer[:read]))
		if err != nil {
			return nil, fmt.Errorf("собрать запрос: %w", err)
		}
		request.Header.Set("X-API-Key", c.key)
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set("X-Chunk-Offset", fmt.Sprint(offset))

		var noted struct {
			ReceivedBytes int64 `json:"received_bytes"`
		}
		if err := c.send(c.upload, request, &noted); err != nil {
			return nil, fmt.Errorf("отправить кусок с %d: %w", offset, err)
		}
		// Двигаемся по тому, что принял сервис, а не по своему счёту: он знает
		// точнее, и после переподключения это спасает от дыры в файле.
		if noted.ReceivedBytes > offset {
			offset = noted.ReceivedBytes
		} else {
			offset += int64(read)
		}
	}

	var job Job
	if err := c.ask(ctx, http.MethodPost, "/v1/uploads/"+upload.ID+"/confirm",
		map[string]any{}, &job); err != nil {
		return nil, fmt.Errorf("собрать запись на сервисе: %w", err)
	}
	return &job, nil
}

// Device — что машина рассказывает о себе.
type Device struct {
	ClientID  string   `json:"client_id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Platform  string   `json:"platform"`
	Version   string   `json:"version"`
	Watching  []string `json:"watching"`
	SentCount int      `json:"sent_count"`
	LastError string   `json:"last_error,omitempty"`
}

// Heartbeat — отметиться на сервисе.
func (c *Client) Heartbeat(ctx context.Context, device Device) error {
	return c.ask(ctx, http.MethodPost, "/v1/devices/heartbeat", device, nil)
}

// LogEntry — строка журнала устройства.
type LogEntry struct {
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"`
	Level string    `json:"level"`
	Text  string    `json:"text"`
}

// SendJournal — отдать сервису свой журнал.
func (c *Client) SendJournal(ctx context.Context, clientID, name string, entries []LogEntry) error {
	return c.ask(ctx, http.MethodPost, "/v1/devices/journal", map[string]any{
		"client_id": clientID,
		"name":      name,
		"events":    entries,
	}, nil)
}

// Meeting — строка списка разобранных встреч.
type Meeting struct {
	Number  int    `json:"number"`
	Emoji   string `json:"emoji"`
	Title   string `json:"title"`
	HeldOn  string `json:"held_on"`
	Minutes *int   `json:"minutes"`
	Tasks   int    `json:"tasks"`
	JobID   string `json:"job_id"`
}

// Meetings — последние разобранные встречи.
func (c *Client) Meetings(ctx context.Context, limit int) ([]Meeting, error) {
	var answer struct {
		Items []Meeting `json:"items"`
	}
	path := fmt.Sprintf("/v1/meetings?limit=%d", limit)
	if err := c.ask(ctx, http.MethodGet, path, nil, &answer); err != nil {
		return nil, err
	}
	return answer.Items, nil
}

// Artifact — текст расшифровки или конспекта.
func (c *Client) Artifact(ctx context.Context, jobID, kind string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/v1/jobs/"+jobID+"/artifacts/"+kind, nil)
	if err != nil {
		return "", fmt.Errorf("собрать запрос: %w", err)
	}
	request.Header.Set("X-API-Key", c.key)
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("сервис не ответил: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return "", fmt.Errorf("прочитать ответ: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("сервис отказал (%d)", response.StatusCode)
	}
	return string(raw), nil
}

// Alive — есть ли связь. Любая ошибка сети — это «нет», а не повод падать.
func (c *Client) Alive(ctx context.Context) bool {
	return c.ask(ctx, http.MethodGet, "/v1/devices", nil, nil) == nil
}

// Event — событие из потока.
type Event struct {
	Name    string
	Payload map[string]any
}

// Stage — как сервис называет стадию задания.
func (e Event) Stage() (key, title string) {
	stage, ok := e.Payload["stage"].(map[string]any)
	if !ok {
		return "", ""
	}
	key, _ = stage["key"].(string)
	title, _ = stage["title"].(string)
	return key, title
}

// Events — слушать поток событий.
//
// Постоянное соединение вместо опроса: о готовности конспекта воркер узнаёт
// сразу. Возвращается, когда поток кончился или контекст отменён; звать
// повторно — забота вызывающего, он же решает, как долго ждать.
func (c *Client) Events(ctx context.Context, onEvent func(Event)) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/events", nil)
	if err != nil {
		return fmt.Errorf("собрать запрос: %w", err)
	}
	request.Header.Set("X-API-Key", c.key)
	request.Header.Set("Accept", "text/event-stream")

	// Своего таймаута у потока нет: он живёт часами, и общий срок обрывал бы
	// его посреди работы.
	stream := &http.Client{}
	response, err := stream.Do(request)
	if err != nil {
		return fmt.Errorf("поток не открылся: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("сервис отказал в потоке (%d)", response.StatusCode)
	}

	reader := bufio.NewReader(response.Body)
	name := ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("поток прервался: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			payload := map[string]any{}
			// Событие с испорченным телом — не повод рвать поток: пропускаем.
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err == nil {
				onEvent(Event{Name: name, Payload: payload})
			}
			name = ""
		}
	}
}

// ── внутреннее ────────────────────────────────────────────────────────────

func (c *Client) ask(ctx context.Context, method, path string, body, into any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("сложить запрос: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return fmt.Errorf("собрать запрос: %w", err)
	}
	request.Header.Set("X-API-Key", c.key)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return c.send(c.http, request, into)
}

func (c *Client) send(through *http.Client, request *http.Request, into any) error {
	response, err := through.Do(request)
	if err != nil {
		return fmt.Errorf("сервис не ответил: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("прочитать ответ: %w", err)
	}
	if response.StatusCode >= 400 {
		return fmt.Errorf("сервис отказал (%d): %s", response.StatusCode, shorten(raw))
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("разобрать ответ: %w", err)
	}
	return nil
}

func shorten(raw []byte) string {
	text := strings.TrimSpace(strings.ReplaceAll(string(raw), "\n", " "))
	if len(text) > 200 {
		return text[:200] + "…"
	}
	return text
}
