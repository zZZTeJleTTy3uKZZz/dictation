package conn

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/atomicfile"
	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/dpapi"
	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/filelock"
)

// ErrSecretNotFound — секрета с таким именем нет.
var ErrSecretNotFound = errors.New("секрет не найден")

// SecretStore — где лежит ключ провайдера. Настройки (Provider) и секрет живут
// отдельно: запись настроек можно показать и передать, и ключа в ней нет.
type SecretStore interface {
	// Get отдаёт секрет; ErrSecretNotFound — такого нет.
	Get(name string) (string, error)
	// Set записывает секрет, заменяя прежний.
	Set(name, value string) error
	// Delete убирает секрет. Отсутствующего секрета не жалко: ошибки нет.
	Delete(name string) error
}

// FileStore — секреты в JSON-файле с правами 0600.
//
// Файл пишется целиком и только через временный файл: оборванная запись не
// должна оставить половину ключей. Формат — плоский объект «имя: секрет»;
// назначение имён задаёт вызывающий.
//
// «Прочитал — изменил — записал» идёт под файловым замком (mybits/filelock):
// окно и CLI пишут секреты в один файл, и без замка запись одного стирала бы
// ключ, который в это же время записал другой. Замок — отдельный файл рядом с
// секретами: блокировать сам файл секретов нельзя, его подменяет
// переименование атомарной записи.
type FileStore struct {
	Path string
}

// lockPath — файл замка рядом с секретами: один на файл секретов.
func (s FileStore) lockPath() string {
	return s.Path + ".lock"
}

// lockWait — сколько ждём замок. Записи короткие (один ключ), а ждать вечно
// нельзя: зависший чужой процесс держал бы окно настроек.
const lockWait = 10 * time.Second

// locked выполняет change под замком файла секретов.
func (s FileStore) locked(change func(values map[string]string) (bool, error)) error {
	if strings.TrimSpace(s.Path) == "" {
		return errors.New("не задан путь к файлу секретов")
	}
	lock, err := filelock.Acquire(s.lockPath(), lockWait)
	if err != nil {
		return fmt.Errorf("секреты: %w", err)
	}
	defer func() { _ = lock.Unlock() }()
	values, err := readSecrets(s.Path)
	if err != nil {
		return err
	}
	write, err := change(values)
	if err != nil || !write {
		return err
	}
	return writeSecrets(s.Path, values)
}

// Get — секрет по имени.
//
// Без замка нарочно: запись подменяется переименованием, и чтение видит либо
// прежний файл, либо новый целиком — ждать чужую запись ключа незачем.
func (s FileStore) Get(name string) (string, error) {
	values, err := readSecrets(s.Path)
	if err != nil {
		return "", err
	}
	value, found := values[name]
	if !found || value == "" {
		return "", ErrSecretNotFound
	}
	return value, nil
}

// Set — записать секрет.
func (s FileStore) Set(name, value string) error {
	if err := checkSecret(name, value); err != nil {
		return err
	}
	return s.locked(func(values map[string]string) (bool, error) {
		values[name] = value
		return true, nil
	})
}

// Delete — убрать секрет. Отсутствующий — не ошибка.
func (s FileStore) Delete(name string) error {
	return s.locked(func(values map[string]string) (bool, error) {
		if _, found := values[name]; !found {
			return false, nil
		}
		delete(values, name)
		return true, nil
	})
}

// DPAPIStore — секреты, зашифрованные DPAPI для текущей учётной записи Windows.
//
// Формат файла тот же, что у FileStore, но значения — base64 от зашифрованного
// куска: файл, унесённый на другую машину или к другому человеку, не
// расшифровывается. Вне Windows Get и Set отказывают dpapi.ErrUnsupported.
type DPAPIStore struct {
	Path string
}

// file — тот же файл, читаемый и записываемый как есть: DPAPIStore только
// шифрует и расшифровывает значения.
func (s DPAPIStore) file() FileStore { return FileStore(s) }

// Get — расшифровать секрет.
func (s DPAPIStore) Get(name string) (string, error) {
	sealed, err := s.file().Get(name)
	if err != nil {
		return "", err
	}
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("секрет %q записан не DPAPI: %w", name, err)
	}
	plain, err := dpapi.Unprotect(data)
	if err != nil {
		return "", fmt.Errorf("секрет %q не расшифрован этой учётной записью: %w", name, err)
	}
	return string(plain), nil
}

// Set — зашифровать и записать секрет.
func (s DPAPIStore) Set(name, value string) error {
	if err := checkSecret(name, value); err != nil {
		return err
	}
	sealed, err := dpapi.Protect([]byte(value))
	if err != nil {
		return fmt.Errorf("секрет %q не защищён DPAPI: %w", name, err)
	}
	return s.file().Set(name, base64.StdEncoding.EncodeToString(sealed))
}

// Delete — убрать секрет. Отсутствующий — не ошибка.
func (s DPAPIStore) Delete(name string) error {
	return s.file().Delete(name)
}

// checkSecret — имя и значение обязательны; пустой ключ — это не ключ.
func checkSecret(name, value string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("имя секрета пусто")
	}
	if strings.TrimSpace(value) == "" {
		return errors.New("секрет пуст — нечего записывать")
	}
	return nil
}

// readSecrets читает файл секретов. Отсутствующий файл — пустой набор, а не
// ошибка: на новой машине секретов ещё никто не заводил. Битый файл — ошибка:
// затирать его первой записью нельзя, в нём могут быть другие ключи.
func readSecrets(path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("не задан путь к файлу секретов")
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("секреты не прочитаны: %w", err)
	}
	values := map[string]string{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("файл секретов %s не разбирается: %w", path, err)
	}
	return values, nil
}

// writeSecrets записывает файл секретов целиком: каталог 0700, файл 0600.
func writeSecrets(path string, values map[string]string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("не задан путь к файлу секретов")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("каталог секретов не создан: %w", err)
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return fmt.Errorf("секреты не собраны: %w", err)
	}
	if err := atomicfile.Write(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("секреты не записаны: %w", err)
	}
	return nil
}
