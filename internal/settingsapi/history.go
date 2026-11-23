// История диктовки для окна: что сказано, что распознано и что можно повторить.
//
// Читается тот же history.json, который пишет служба: второго списка нет и быть
// не должно. Неудачный разбор остаётся в истории строкой со статусом «не
// распознано» и звуком — его видно, его можно послушать и разобрать ещё раз.
package settingsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/localdict"
)

// HistoryView — одна строка истории, как её показывает окно.
type HistoryView struct {
	At     string `json:"at"`
	When   string `json:"when"`
	Length string `json:"length"`
	Text   string `json:"text"`
	// Status — «распознано», «не распознано» или «восстановлено».
	Status string `json:"status"`
	// Audio — звук строки. Пусто — слушать нечего: фраза уже распознана.
	Audio string `json:"audio"`
	// CanRetry — у строки есть звук и нет текста: её можно разобрать ещё раз.
	CanRetry bool `json:"can_retry"`
}

// History — история диктовки, свежие впереди.
func History(limit int) ([]HistoryView, error) {
	path := filepath.Join(localdict.SettingsDir(), "history.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []HistoryView{}, nil
		}
		return nil, err
	}
	var entries []localdict.Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("история диктовки не читается: %w", err)
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	shown := make([]HistoryView, 0, len(entries))
	for _, entry := range entries {
		shown = append(shown, showHistory(entry))
	}
	return shown, nil
}

func showHistory(entry localdict.Entry) HistoryView {
	status := "распознано"
	switch entry.Destination {
	case "failed":
		status = "не распознано"
	case "restored":
		status = "восстановлено"
	}
	// Автостоп — остановка по тишине, а не распознавание: без текста строка
	// не «распознано» и не ошибка, а пояснение, почему запись кончилась.
	if entry.Status == "autostop" {
		status = "остановлено: нет голоса"
	}
	text := strings.TrimSpace(entry.Text)
	// Пустой текст со звуком — это и есть нераспознанная фраза: старые записи
	// могли получить статус ещё до появления признака failed.
	canRetry := text == "" && entry.Audio != "" && fileExists(entry.Audio)
	if text == "" && entry.Audio != "" {
		status = "не распознано"
	}
	return HistoryView{
		At:       entry.At.Format("02.01.2006 15:04"),
		When:     ago(entry.At),
		Length:   length(entry.Seconds),
		Text:     text,
		Status:   status,
		Audio:    entry.Audio,
		CanRetry: canRetry,
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// RetryHistory просит службу разобрать сохранённую запись ещё раз.
//
// Разбор идёт в службе: у неё ключи, модель и буфер обмена. Окно только просит и
// перечитывает историю — само оно ничего не распознаёт и не вставляет, иначе
// правила разбора разошлись бы между двумя программами.
func RetryHistory(audio string) error {
	if strings.TrimSpace(audio) == "" {
		return fmt.Errorf("у записи нет звука")
	}
	return localdict.RetryRecording(context.Background(), audio)
}
