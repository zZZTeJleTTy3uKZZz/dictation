package localdict

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// В этот журнал попадают только фиксированные события, счётчики, длительности
// и отметки сторожей. Никаких расшифровок, тел запросов и ключей.
//
// Исключение — «speech»: строки переноса ключей распознавания (speech.SetLog).
// Это состояния хранилища ключей — «другой ключ под именем», «ключ не сошёлся», —
// без значений секретов; их пишет ядро, и проверки следят, что ключ туда не
// попадает. Причина отказа сервиса по-прежнему уходит в историю, а не сюда.
func (a *App) sessionLog(event string, metadata string) {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	logLine(a.Dir, event, metadata)
}

// logLine дописывает строку в журнал сеанса рядом с записями.
//
// Вынесено из sessionLog, чтобы писать в ТОТ ЖЕ журнал мог и Audio: у него нет
// ни каталога, ни замка, а каталог выводится из пути записи, который он и так
// получает. Заводить Audio собственный журнал нельзя — это вторая копия правил
// прокрутки, и однажды они разойдутся (в этом проекте так уже было с правилами
// настроек).
//
// Замок здесь общий на App, и Audio пишет без него. Строка дописывается одним
// вызовом, поэтому сама она не порвётся; а если две прокрутки совпадут, потеряется
// одна прокрутка, а не данные. Ради этого городить общий замок между App и Audio
// не стоит.
//
// ВАЖНО: старый файл может остаться от прежней версии с чужими правами. Молча
// пропустить строку нельзя — так на ноутбуке 1.13 журнал замер 11.11, хотя
// service.log и supervisor.log в том же каталоге писались каждый день. При
// отказе открытия пробуем запасной каталог и говорим о причине в stderr —
// надзиратель перенаправляет его в daemon-stderr.log, а служба видит тот же
// текст рядом со своим журналом.
func logLine(dir, event, metadata string) {
	path := filepath.Join(dir, "dictation.log")
	line := fmt.Sprintf("%s event=%s %s", time.Now().Format(time.RFC3339Nano), event, metadata)
	primaryErr := appendLogLine(path, line)
	if primaryErr == nil {
		return
	}

	fallback := ""
	for _, where := range logFallbackDirs(dir) {
		other := filepath.Join(where, "dictation.log")
		if appendLogLine(other, line) == nil {
			fallback = other
			break
		}
	}
	reportLogProblem(path, primaryErr, fallback)
}

// appendLogLine — одна строка с ограниченным размером файла. Ошибку возвращаем
// вызывающему: решать, где писать дальше, он должен здесь, а не в каждом из
// пользователей журнала.
func appendLogLine(path, line string) error {
	if info, err := os.Stat(path); err == nil && info.Size() >= 256*1024 {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(file, line)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// logFallbackDirs — каталоги, куда можно писать, если основной файл занят
// чужим ACL. Сначала личные данные программы (обычно LOCALAPPDATA), потом
// временный каталог процесса. Оба пути подменяются в проверках.
var logFallbackDirs = func(primary string) []string {
	var dirs []string
	if data := DataDir(); !samePath(data, primary) {
		dirs = append(dirs, data)
	}
	if tmp := os.TempDir(); !samePath(tmp, primary) {
		dirs = append(dirs, tmp)
	}
	return dirs
}

var reportedLogPaths sync.Map

// reportLogProblem пишет причину один раз на путь за процесс: иначе отказ
// прав превратил бы каждую фразу в новую строку stderr.
var reportLogProblem = func(path string, err error, fallback string) {
	if _, seen := reportedLogPaths.LoadOrStore(path, true); seen {
		return
	}
	if fallback == "" {
		fmt.Fprintf(os.Stderr, "журнал диктовки: не удалось открыть %s (%v), запасного каталога нет\n", path, err)
		return
	}
	fmt.Fprintf(os.Stderr, "журнал диктовки: не удалось открыть %s (%v), пишем в %s\n", path, err, fallback)
}
