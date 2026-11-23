// Небольшие входы для чужого окна: повторить разбор сохранённой записи.
package localdict

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// daemonDirs — где искать сведения о запущенной службе: сперва служебный
// профиль (служба работает от системы), потом личная папка человека (задача
// планировщика и сеансовая половина).
func daemonDirs() []string {
	// Сперва личная папка: если двигатель поднят в сеансе человека, он там. В
	// служебном профиле ищем, когда личного нет: служба работает от системы.
	//
	// Подменённый DICTATION_DATA_DIR означает «это чужой, проверочный мир»: в
	// служебный профиль тогда не смотрим, иначе проверка нашла бы настоящую
	// службу машины и попросила её скачать модель.
	dirs := []string{DataDir()}
	if os.Getenv("DICTATION_DATA_DIR") != "" {
		return dirs
	}
	if system := os.Getenv("SystemRoot"); system != "" {
		dirs = append(dirs, filepath.Join(system, "System32", "config", "systemprofile", "AppData", "Local", "Dictation"))
	}
	return dirs
}

// liveDaemon — сведения о живом движке. Мёртвый endpoint пропускается: после
// падения файл остаётся, и просить по нему текст значит ждать в пустоту.
func liveDaemon(ctx context.Context) (Endpoint, error) {
	for _, dir := range daemonDirs() {
		endpoint, err := publishedEndpoint(dir)
		if err != nil {
			continue
		}
		if healthy(ctx, endpoint) {
			return endpoint, nil
		}
	}
	return Endpoint{}, errors.New("служба диктовки не запущена")
}

// postDaemon — короткая просьба к службе с её ключом. Тело пустое — запрос без
// тела; иначе JSON.
func postDaemon(ctx context.Context, endpoint Endpoint, path string, body any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("собрать запрос: %w", err)
		}
		reader = strings.NewReader(string(raw))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL+path, reader)
	if err != nil {
		return fmt.Errorf("собрать запрос: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("служба диктовки не ответила: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		reason := strings.TrimSpace(string(said))
		if reason == "" {
			reason = response.Status
		}
		return errors.New(reason)
	}
	return nil
}

// RetryRecording просит службу разобрать сохранённую запись ещё раз — кнопка
// «Отправить ещё раз» в истории. Текст при удаче служба кладёт в историю и в
// буфер обмена сама: разбор идёт там же, где обычно.
func RetryRecording(ctx context.Context, audio string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint, err := liveDaemon(ctx)
	if err != nil {
		return errors.New("служба диктовки не запущена — повтор станет доступен после её запуска")
	}
	// Разбор с повторами идёт минутами: ждём дольше обычного здоровья службы.
	ask, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	return postDaemon(ask, endpoint, "/retry", map[string]string{"audio": audio})
}
