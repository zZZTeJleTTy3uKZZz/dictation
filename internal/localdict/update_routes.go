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

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/updates"
)

// VersionCode — version_code установленной версии по правилу протокола:
// major*10000+minor*100+patch (1.13.3 → 11303). По нему, а не по строке
// версии, решается обязательность обновления (§5).
const VersionCode = 11303

// updateManifestURL — адрес манифеста: переменная среды для разработки,
// иначе постоянный адрес Generic Package из пакета updates. Сборка может
// переопределить updates.ManifestURL через -X, не трогая кода службы.
func updateManifestURL() string {
	if url := strings.TrimSpace(os.Getenv("DICTATION_UPDATE_MANIFEST")); url != "" {
		return url
	}
	return updates.ManifestURL
}

// startUpdates поднимает обновление вместе со службой.
//
// Проверка при старте идёт не мгновенно: полминуты — чтобы поднялись запись,
// значок и окно, и лишь потом служба пошла в сеть. Потом — раз в шесть часов.
// Идущая диктовка проверке не мешает (это только чтение манифеста), а установка
// её ждёт: см. updates.Controller.Install.
func (a *App) startUpdates() {
	if a.update != nil {
		return
	}
	a.update = updates.New(updates.Options{
		AllUsers:       servicePresent,
		ManifestURL:    updateManifestURL(),
		Token:          os.Getenv("DICTATION_UPDATE_TOKEN"),
		CurrentVersion: Version,
		CurrentCode:    VersionCode,
		Dir:            filepath.Join(a.Media, "update"),
		Busy:           func() bool { return a.Snapshot().RecordingActive },
		Logger:         a.sessionLog,
		InitialDelay:   30 * time.Second,
	})
	a.update.Start(a.ctx)
}

// updateRoutes — HTTP API обновления для окна (§6 протокола).
//
// Одно состояние на всю службу: окно не хранит своей копии, а спрашивает
// GET /update. Проверка манифеста идёт в фоне: ответ на POST /update/check
// возвращается сразу, иначе медленная сеть держала бы окно.
func (a *App) updateRoutes(mux *http.ServeMux) {
	write := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		if a.update == nil {
			_ = json.NewEncoder(w).Encode(updates.State{Status: updates.StatusIdle})
			return
		}
		_ = json.NewEncoder(w).Encode(a.update.State())
	}
	ready := func(w http.ResponseWriter) bool {
		if a.update == nil {
			http.Error(w, "обновление не поднято", http.StatusServiceUnavailable)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /update", func(w http.ResponseWriter, r *http.Request) { write(w) })
	mux.HandleFunc("POST /update/check", func(w http.ResponseWriter, r *http.Request) {
		if !ready(w) {
			return
		}
		go a.update.Check(a.ctx)
		write(w)
	})
	mux.HandleFunc("POST /update/download", func(w http.ResponseWriter, r *http.Request) {
		if !ready(w) {
			return
		}
		a.update.Download()
		write(w)
	})
	mux.HandleFunc("POST /update/install", func(w http.ResponseWriter, r *http.Request) {
		if !ready(w) {
			return
		}
		// Контекст запроса живёт, пока обработчик не ответил; установщик — нет.
		// Привязать его к r.Context() значило бы убить тихую установку сразу
		// после ответа окну. Живёт до конца работы службы (a.ctx).
		a.update.Install(a.ctx)
		write(w)
	})
	mux.HandleFunc("POST /update/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !ready(w) {
			return
		}
		a.update.Cancel()
		write(w)
	})
}

// Короткие входы для значка лотка: он отдельная программа и своего состояния
// обновления не держит, а спрашивает службу по её адресу — как окно.
//
// ПОЧЕМУ ТОЛЬКО ЧТЕНИЕ И ЗАПУСК. Манифест проверяет и файл скачивает служба, у
// которой есть Busy (идёт ли диктовка). Значок лишь просит и показывает ответ:
// отказ или отложенная установка приходят в Message состояния, а не новой
// ошибкой.

// UpdateState — состояние обновления службы (GET /update).
func UpdateState(ctx context.Context) (updates.State, error) {
	return updateCall(ctx, http.MethodGet, "/update")
}

// UpdateCheck — попросить службу проверить манифест; ответ приходит сразу, а
// результат появится в UpdateState.
func UpdateCheck(ctx context.Context) (updates.State, error) {
	return updateCall(ctx, http.MethodPost, "/update/check")
}

// UpdateDownload — начать скачивание найденной версии.
func UpdateDownload(ctx context.Context) (updates.State, error) {
	return updateCall(ctx, http.MethodPost, "/update/download")
}

// UpdateInstall — поставить скачанную версию. Служба сама отложит установку,
// если идёт запись, и вернёт это в Message.
func UpdateInstall(ctx context.Context) (updates.State, error) {
	return updateCall(ctx, http.MethodPost, "/update/install")
}

// updateCall — один запрос к маршруту обновления службы с её ключом.
func updateCall(ctx context.Context, method, path string) (updates.State, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint, err := liveDaemon(ctx)
	if err != nil {
		return updates.State{}, errors.New("служба диктовки не запущена — обновление станет доступно после её запуска")
	}
	// Проверка манифеста идёт в сети; сам маршрут отвечает сразу, так что
	// короткого предела хватает и для медленного соединения.
	ask, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ask, method, endpoint.URL+path, nil)
	if err != nil {
		return updates.State{}, fmt.Errorf("собрать запрос: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return updates.State{}, fmt.Errorf("служба диктовки не ответила: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		reason := strings.TrimSpace(string(said))
		if reason == "" {
			reason = response.Status
		}
		return updates.State{}, errors.New(reason)
	}
	var state updates.State
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		return updates.State{}, fmt.Errorf("ответ службы не разобран: %w", err)
	}
	return state, nil
}
