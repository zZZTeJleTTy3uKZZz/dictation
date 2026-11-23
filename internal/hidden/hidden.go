// Package hidden — запуск чужих программ без окна консоли.
//
// ЗАЧЕМ ОТДЕЛЬНЫЙ ПАКЕТ РАДИ ДВУХ ПОЛЕЙ. Программа живёт в фоне: значок в лотке,
// задача планировщика. Любой запуск чужой программы из неё — ffmpeg,
// распознаватель, проигрыватель — по умолчанию открывает окно консоли. Оно
// выскакивает поверх всего, посреди встречи или посреди диктовки, и человек
// видит чёрный прямоугольник вместо работы.
//
// Забыть эти два поля в одном месте из восьми — значит получить ровно ту же
// вспышку консоли, но уже там, где её не ждали. Поэтому в этой программе чужие
// команды собираются ТОЛЬКО здесь, а exec.Command напрямую считается дефектом.
package hidden

import (
	"context"
	"os/exec"
	"syscall"
)

// createNoWindow — CREATE_NO_WINDOW: у процесса не будет своей консоли.
//
// HideWindow один не спасает: он прячет окно, которое уже создано, и на быстрых
// программах человек успевает увидеть вспышку.
const createNoWindow = 0x08000000

// Command — команда, которая не покажет консоль.
func Command(name string, args ...string) *exec.Cmd {
	return quiet(exec.Command(name, args...))
}

// CommandContext — то же, но с отменой: долгие программы (распознавание,
// сведение записи) обязаны умирать вместе с контекстом, а не переживать его.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return quiet(exec.CommandContext(ctx, name, args...))
}

func quiet(command *exec.Cmd) *exec.Cmd {
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return command
}
