// Package filelock — исключительная блокировка файла между процессами:
// один экземпляр демона, одна запись в очередь, один сборщик.
//
// Linux и macOS — flock, Windows — LockFileEx. Блокировку снимает система,
// когда процесс умирает, поэтому «залипших» замков после падения нет — в
// отличие от pid-файлов. Файл замка никогда не удаляется: удаление снимало
// бы замок следующего владельца.
//
// Внутри процесса горутины разводит мьютекс на путь: замок ОС разводит
// процессы, мьютекс — горутины одного процесса (на Windows второй дескриптор
// того же процесса иначе получил бы отказ, а на Linux — замок поверх своего).
// Ожидание с пределом (Acquire) — для коротких «прочитал-изменил-записал»,
// где отказ без ожидания был бы ложным. Перенесено из crew (#5541).
package filelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrLocked — замок держит другой процесс (или другая горутина этого же).
var ErrLocked = errors.New("filelock: занято")

// retryStep — пауза между попытками взять занятый замок в Acquire.
const retryStep = 5 * time.Millisecond

// Lock — взятый замок.
type Lock struct {
	f    *os.File
	mu   *sync.Mutex
	once sync.Once
	err  error
}

var pathMutexes sync.Map // путь → *sync.Mutex

func pathMutex(path string) *sync.Mutex {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	v, _ := pathMutexes.LoadOrStore(abs, &sync.Mutex{})
	mu, _ := v.(*sync.Mutex) // в карте только *sync.Mutex
	return mu
}

// TryLock берёт замок без ожидания; занято — ErrLocked.
func TryLock(path string) (*Lock, error) {
	mu := pathMutex(path)
	if !mu.TryLock() {
		return nil, ErrLocked
	}
	f, err := open(path)
	if err == nil {
		err = lockFile(f, false)
		if err != nil {
			_ = f.Close()
		}
	}
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	return &Lock{f: f, mu: mu}, nil
}

// Wait берёт замок, ожидая сколько угодно, пока его отпустят.
func Wait(path string) (*Lock, error) {
	mu := pathMutex(path)
	mu.Lock()
	f, err := open(path)
	if err == nil {
		err = lockFile(f, true)
		if err != nil {
			_ = f.Close()
		}
	}
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	return &Lock{f: f, mu: mu}, nil
}

// Acquire ждёт замок не дольше wait (≤ 0 — одна попытка); не дождался —
// ошибка, обёрнутая вокруг ErrLocked.
func Acquire(path string, wait time.Duration) (*Lock, error) {
	mu := pathMutex(path)
	deadline := time.Now().Add(wait)
	for !mu.TryLock() {
		if wait <= 0 || time.Now().After(deadline) {
			return nil, fmt.Errorf("замок %s: %w", path, ErrLocked)
		}
		time.Sleep(retryStep)
	}
	for {
		f, err := open(path)
		if err == nil {
			if err = lockFile(f, false); err == nil {
				return &Lock{f: f, mu: mu}, nil
			}
			_ = f.Close()
		}
		if !errors.Is(err, ErrLocked) {
			mu.Unlock()
			return nil, err
		}
		if wait <= 0 || time.Now().After(deadline) {
			mu.Unlock()
			return nil, fmt.Errorf("замок %s занят другим процессом: %w", path, ErrLocked)
		}
		time.Sleep(retryStep)
	}
}

func open(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("filelock: %w", err)
	}
	return f, nil
}

// Unlock отпускает замок; повторный вызов безопасен и возвращает тот же итог.
func (l *Lock) Unlock() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.f != nil {
			l.err = unlockFile(l.f)
			if cerr := l.f.Close(); l.err == nil {
				l.err = cerr
			}
		}
		if l.mu != nil {
			l.mu.Unlock()
		}
	})
	return l.err
}
