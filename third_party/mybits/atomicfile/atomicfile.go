// Package atomicfile — запись файла целиком или никак.
//
// Читатель видит либо старое содержимое, либо новое, но не обрывок: данные
// пишутся во временный файл рядом, сбрасываются на диск и переименовываются
// поверх. На Windows os.Rename заменяет существующий файл (MoveFileEx с
// MOVEFILE_REPLACE_EXISTING), поэтому поведение одно на всех системах.
//
// Появился потому, что двенадцать копий этой функции жили в девяти
// репозиториях, и в половине не было fsync: после сбоя питания оставался
// пустой файл настроек.
//
// На Windows замена отказывает, пока другой процесс держит файл открытым
// (ERROR_ACCESS_DENIED / ERROR_SHARING_VIOLATION): читатель состояния или
// антивирус. Такая занятость — на миллисекунды, поэтому замена повторяется с
// нарастающей паузой до RenameWait; вечный отказ возвращается как есть.
// Урок crew: без повторов терялся heartbeat и отказывал автоподъём.
package atomicfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// RenameWait — сколько ждать, пока чужой открытый дескриптор отпустит файл.
var RenameWait = 5 * time.Second

func renameRetry(from, to string) error {
	deadline := time.Now().Add(RenameWait)
	pause := 10 * time.Millisecond
	for {
		err := os.Rename(from, to)
		if err == nil || !busy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(pause)
		pause = min(pause*2, 250*time.Millisecond)
	}
}

// Write записывает data в path с правами perm.
func Write(path string, data []byte, perm os.FileMode) error {
	return WriteFrom(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// WriteFrom записывает то, что fill пишет в поток. Ошибка fill оставляет
// прежний файл нетронутым.
func WriteFrom(path string, perm os.FileMode, fill func(io.Writer) error) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("atomicfile: временный файл: %w", err)
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if err = fill(tmp); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("atomicfile: сброс на диск: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("atomicfile: закрытие: %w", err)
	}
	if err = os.Chmod(name, perm); err != nil {
		return fmt.Errorf("atomicfile: права: %w", err)
	}
	if err = renameRetry(name, path); err != nil {
		return fmt.Errorf("atomicfile: замена: %w", err)
	}
	syncDir(dir)
	return nil
}
