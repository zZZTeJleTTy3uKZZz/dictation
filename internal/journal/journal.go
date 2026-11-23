// Package journal — журнал воркера на диске.
//
// Порт meetdigest_watcher/journal.py.
//
// ЗАЧЕМ ФАЙЛ. Воркер живёт в фоне, без консоли: задача планировщика запускает
// его через процесс без окна, и «посмотреть вывод» негде. Журнал с ротацией —
// единственное, по чему потом разбираются, что произошло.
package journal

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

const (
	// maxSize — при каком размере начинать новый файл.
	//
	// Мегабайт: столько воркер набирает за несколько дней, и такой файл ещё
	// читается глазами целиком.
	maxSize = 1 << 20
	// keepOld — сколько прежних файлов держим.
	keepOld = 2
)

// Setup — открыть журнал и вернуть логгер.
//
// Пишет и в файл, и в стандартный вывод: в фоне полезен файл, при запуске
// руками — вывод, и держать для этого два режима незачем.
func Setup(path, who string) (*slog.Logger, func(), error) {
	if path == "" {
		return slog.New(slog.NewTextHandler(os.Stdout, nil)), func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("завести папку журнала: %w", err)
	}
	if err := rotate(path); err != nil {
		return nil, nil, err
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("открыть журнал: %w", err)
	}

	handler := slog.NewTextHandler(io.MultiWriter(file, os.Stdout), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	log := slog.New(handler).With("кто", who)
	return log, func() { _ = file.Close() }, nil
}

// rotate — сдвинуть файлы, если журнал вырос.
//
// Без этого он растёт до гигабайтов и однажды занимает диск ровно в тот день,
// когда нужен для разбора поломки.
func rotate(path string) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxSize {
		return nil
	}
	// Самый старый файл выбрасываем, остальные сдвигаем на одну ступень.
	oldest := fmt.Sprintf("%s.%d", path, keepOld)
	_ = os.Remove(oldest)
	for index := keepOld - 1; index >= 1; index-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, index), fmt.Sprintf("%s.%d", path, index+1))
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return fmt.Errorf("сдвинуть журнал: %w", err)
	}
	return nil
}
