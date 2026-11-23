// Package trayupdate — что значку лотка нужно от обновления «Диктовки», без окна
// и без Windows: какой пункт когда показан, что написано в строке и подсказке,
// как проверка и установка ждут ответа службы.
//
// ПОЧЕМУ ОТДЕЛЬНЫЙ ПАКЕТ. Значок собирается только под Windows и тянет обвязку
// лотка, и проверить на нём логику без машины владельца нельзя. Здесь решения и
// тексты — их проверяет любой прогон тестов. Сеть подменяется интерфейсом
// Service: в значке его реализует localdict, в тестах — подставной сервис.
package trayupdate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/updates"
)

// ErrSlow — служба дольше предела не отдала итог. Значок не виснет: человек
// увидит, что служба занята, и сможет нажать ещё раз.
var ErrSlow = errors.New("служба обновления долго не отвечает — попробуйте позже")

// busyMessage — что видит человек, когда установка ждёт конца записи.
const busyMessage = "Идёт запись — обновление поставится после её конца"

// Service — то, о чём значок просит службу. Настоящая реализация — функции
// localdict.Update*; в тестах подменяется.
type Service interface {
	State(ctx context.Context) (updates.State, error)
	Check(ctx context.Context) (updates.State, error)
	Download(ctx context.Context) (updates.State, error)
	Install(ctx context.Context) (updates.State, error)
}

// Step — что делает пункт «Обновить до …» по состоянию службы.
type Step int

const (
	// StepNone — пункта нет: обновлять пока нечего.
	StepNone Step = iota
	// StepDownload — версия найдена, файла ещё нет: сначала скачать.
	StepDownload
	// StepInstall — файл скачан и проверен: можно ставить.
	StepInstall
	// StepWait — скачивание или установка уже идут: второй раз не запускаем.
	StepWait
)

// ActionFor решает, что делает пункт, по состоянию службы.
func ActionFor(state updates.State) Step {
	switch state.Status {
	case updates.StatusAvailable:
		return StepDownload
	case updates.StatusReady:
		return StepInstall
	case updates.StatusDownloading, updates.StatusInstalling:
		return StepWait
	default:
		return StepNone
	}
}

// Offer — показывать ли пункт «Обновить до X.Y.Z». Пункт виден только тогда,
// когда есть версия, на которую можно обновиться; во время скачивания и
// установки его прячем, чтобы не нажали дважды.
func Offer(state updates.State) bool {
	step := ActionFor(state)
	return (step == StepDownload || step == StepInstall) && state.Version != ""
}

// InstallTitle — подпись пункта установки.
func InstallTitle(state updates.State) string {
	return "Обновить до " + state.Version
}

// Badge — нужна ли значку заметка «есть обновление». Ставится уже идущая
// установка — заметку убираем: программа вот-вот перезапустится.
func Badge(state updates.State) bool {
	switch state.Status {
	case updates.StatusAvailable, updates.StatusDownloading, updates.StatusReady:
		return state.Version != ""
	default:
		return false
	}
}

// Tooltip — подсказка при наведении на значок с заметкой; пусто — подсказка
// по умолчанию.
func Tooltip(state updates.State) string {
	if !Badge(state) {
		return ""
	}
	return "Диктовка — доступна версия " + state.Version
}

// Line — строка статуса в меню. Пусто — строки нет.
//
// callErr — ответ службы не получен (служба не запущена, маршрут не ответил):
// тогда показываем причину, а не старое состояние.
func Line(state updates.State, callErr error) string {
	if callErr != nil {
		return "Обновление недоступно: " + callErr.Error()
	}
	// Текст ядра важнее общих слов: «установка после записи», «для всех пока
	// не поддерживается», причина отказа — человек должен прочесть именно его.
	if state.Message != "" && (state.Status == updates.StatusReady || state.Status == updates.StatusFailed) {
		return state.Message
	}
	switch state.Status {
	case updates.StatusChecking:
		return "Проверяю обновление…"
	case updates.StatusUpToDate:
		return "Обновлений нет"
	case updates.StatusAvailable:
		line := "Доступна версия " + state.Version
		if state.Mandatory {
			line += " (обязательное)"
		}
		return line
	case updates.StatusDownloading:
		line := "Скачиваю версию " + state.Version
		if state.Total > 0 {
			line += fmt.Sprintf(" — %d%%", state.Received*100/state.Total)
		}
		return line
	case updates.StatusReady:
		return "Версия " + state.Version + " скачана, можно ставить"
	case updates.StatusInstalling:
		return "Устанавливаю версию " + state.Version + "…"
	case updates.StatusFailed:
		if state.Message != "" {
			return state.Message
		}
		return "Обновление не проверилось"
	default:
		return ""
	}
}

// Check просит службу проверить манифест и ждёт итога.
//
// Служба отвечает сразу, а сама проверка идёт в фоне: поэтому сначала даём ей
// pause, потом читаем состояние, пока оно в «проверяю». Предел limit — чтобы
// зависший узел не держал значок вечно.
func Check(ctx context.Context, svc Service, pause, limit time.Duration) (updates.State, error) {
	if _, err := svc.Check(ctx); err != nil {
		return updates.State{}, err
	}
	return waitWhile(ctx, svc, pause, limit, updates.StatusChecking)
}

// Update — пункт «Обновить до …»: скачать, если файла ещё нет, и поставить.
//
// busy — идёт диктовка. Установку в этот миг не запускаем вовсе: служба и так
// отложила бы её, но человек увидит причину, а не молчание.
func Update(ctx context.Context, svc Service, state updates.State, busy bool, pause, limit time.Duration) (updates.State, error) {
	switch ActionFor(state) {
	case StepNone, StepWait:
		return state, nil
	case StepDownload:
		if _, err := svc.Download(ctx); err != nil {
			return updates.State{}, err
		}
		done, err := waitWhile(ctx, svc, pause, limit, updates.StatusDownloading)
		if err != nil {
			return done, err
		}
		if done.Status != updates.StatusReady {
			// Скачивание не вышло (отмена, ошибка сети): показываем, что
			// вышло, а не ставим то, чего нет.
			return done, nil
		}
		state = done
	}
	if busy {
		state.Message = busyMessage
		return state, nil
	}
	return svc.Install(ctx)
}

// waitWhile читает состояние, пока оно равно status. Первое чтение — после
// pause: служба ответила раньше, чем её фоновая нить сменила состояние.
func waitWhile(ctx context.Context, svc Service, pause, limit time.Duration, status string) (updates.State, error) {
	deadline := time.Now().Add(limit)
	for {
		select {
		case <-ctx.Done():
			return updates.State{}, ctx.Err()
		case <-time.After(pause):
		}
		state, err := svc.State(ctx)
		if err != nil {
			return updates.State{}, err
		}
		if state.Status != status {
			return state, nil
		}
		if time.Now().After(deadline) {
			return state, ErrSlow
		}
	}
}
