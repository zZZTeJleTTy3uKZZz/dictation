// Список встреч, сохранённый на диск.
//
// ЗАЧЕМ. Меню должно быть полным в тот миг, когда по значку нажали, — значит к
// этому моменту список уже обязан лежать готовым. Своего хранилища заводить
// незачем: один файл рядом с настройками.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/client"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/config"
)

// cachePath — тот же файл, что у Python-версии: значки не ходят парой, но
// человек может поставить обе сборки и не должен увидеть разницы.
func cachePath() string {
	return filepath.Join(config.Home(), "meetings-cache.json")
}

func readCache() []client.Meeting {
	raw, err := os.ReadFile(cachePath())
	if err != nil {
		return nil
	}
	var stored struct {
		Items []client.Meeting `json:"items"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		// Испорченный список — пустое меню до первого обхода, а не отказ значка.
		return nil
	}
	return stored.Items
}

func writeCache(meetings []client.Meeting) {
	raw, err := json.MarshalIndent(struct {
		Items []client.Meeting `json:"items"`
	}{meetings}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(cachePath()), 0o755); err != nil {
		return
	}
	// Потеря списка ничего не стоит: через минуту нить спросит снова.
	_ = os.WriteFile(cachePath(), append(raw, '\n'), 0o644)
}
