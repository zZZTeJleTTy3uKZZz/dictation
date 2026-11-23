package localdict

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/proc"

	"golang.org/x/sys/windows"
)

// RunSupervisor owns one daemon tree. Exit code zero is an intentional quit;
// crashes and repeated missed health checks restart the tree with backoff.
func RunSupervisor(ctx context.Context, executable, dir string, settings bool) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// The installer writes this entry once; a cleanup tool, a partial uninstall
	// or a restored profile can drop it, and then a restart leaves the user with
	// no dictation at all. Repair it before owning the daemon.
	if repaired, err := EnsureAutostart(executable); err != nil {
		supervisorLog(dir, "startup entry could not be verified: "+err.Error())
	} else if repaired {
		supervisorLog(dir, "startup entry restored for the current user")
	}
	// Надзирателю не нужны ни история, ни звук, ни окно: он запускает службу и
	// проверяет здоровье. Маленький предел кучи и частая сборка держат его в
	// единицах мегабайт, а рабочий набор отдаётся системе сразу и потом в
	// простое (memory.go).
	tuneSupervisorMemory()
	backoff := time.Second
	// The endpoint is per-user state and stays in the profile while preferences
	// live outside it.
	media := DataDir()
	standing := false
	// Первый такт обрезает сразу: запуск службы — это и есть та работа, после
	// которой в надзирателе остаются тронутые страницы.
	lastTrim := time.Time{}
	for {
		// В простое и в ожидании чужого движка делать нечего: страницы, к
		// которым никто не притрагивается, отдаём системе.
		if lastTrim.IsZero() || time.Since(lastTrim) >= supervisorTrimEvery {
			freeOSMemory()
			_ = trimOwnMemory()
			lastTrim = time.Now()
		}
		// Диктовку уже ведёт движок из старшего сеанса — свой поднимать нельзя:
		// два движка на одну машину пишут в один endpoint.json, и каждый
		// надзиратель считает чужого мёртвым. Ждём здесь, на месте: уходить
		// нельзя, иначе после выхода человека из сеанса службы не останется
		// вовсе (см. daemonOwner).
		if owner, held := daemonOwner(media, 0); held && senior(owner, os.Getpid()) {
			if !standing {
				supervisorLog(dir, fmt.Sprintf("dictation is held by a daemon in a senior session pid=%d; standing by", owner))
				standing = true
			}
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		standing = false
		args := []string{"--daemon"}
		if settings {
			args = append(args, "--settings")
		}
		// Поток ошибок службы — в файл рядом: без него «daemon stopped
		// unexpectedly» не говорит, почему служба упала. Размер ограничен.
		stderrPath := filepath.Join(dir, "daemon-stderr.log")
		command, stderrFile, err := startDaemon(executable, args, stderrPath)
		if err != nil {
			return err
		}
		closeStderr := func() {
			if stderrFile != nil {
				_ = stderrFile.Close()
				stderrFile = nil
			}
		}
		if stderrFile != nil {
			_, _ = fmt.Fprintf(stderrFile, "\n--- started %s pid=%d ---\n", time.Now().UTC().Format(time.RFC3339), command.Process.Pid)
		}
		supervisorLog(dir, fmt.Sprintf("started daemon pid=%d", command.Process.Pid))
		// Задание нужно не для жизни, а для порядка: зависший движок гасится
		// вместе со своими помощниками, а не оставляет их сиротами.
		job := treeJob(command.Process.Pid, dir)
		kill := func() {
			if job != 0 {
				_ = windows.TerminateJobObject(job, 1)
				return
			}
			_ = command.Process.Kill()
		}
		settings = false
		ended := make(chan error, 1)
		go func() { ended <- command.Wait() }()
		ticker := time.NewTicker(2 * time.Second)
		started := time.Now()
		failures := 0
		hung := false
		stepped := false
		running := true
		for running {
			select {
			case err = <-ended:
				running = false
			case <-ctx.Done():
				kill()
				<-ended
				ticker.Stop()
				closeTree(job)
				closeStderr()
				return ctx.Err()
			case <-ticker.C:
				if time.Since(lastTrim) >= supervisorTrimEvery {
					freeOSMemory()
					_ = trimOwnMemory()
					lastTrim = time.Now()
				}
				// Появился чужой движок из старшего сеанса: убираем свой и
				// встаём в ожидание. Без этой проверки два надзирателя — из
				// задачи в сеансе 0 и из автозапуска в сеансе человека —
				// убивали движки друг друга каждые двадцать секунд, и диктовка
				// длиннее нескольких секунд теряла разговор.
				if owner, held := daemonOwner(media, command.Process.Pid); held && senior(owner, command.Process.Pid) {
					supervisorLog(dir, fmt.Sprintf("dictation is held by a daemon in a senior session pid=%d; standing by", owner))
					// Дальше мы встанем на место и будем молча ждать чужого
					// ухода — писать про это второй раз незачем.
					standing = true
					stepped, hung = true, true
					kill()
					err = <-ended
					running = false
					break
				}
				if time.Since(started) < 8*time.Second {
					continue
				}
				if daemonHealthy(ctx, media, command.Process.Pid) {
					failures = 0
				} else {
					failures++
				}
				if failures >= 3 {
					supervisorLog(dir, "daemon missed three health checks; restarting")
					hung = true
					kill()
					err = <-ended
					running = false
				}
			}
		}
		ticker.Stop()
		closeTree(job)
		closeStderr()
		if stepped {
			continue
		}
		if err == nil && !hung {
			supervisorLog(dir, "intentional quit")
			return nil
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		reason := "daemon stopped unexpectedly"
		if hung {
			reason = "daemon missed three health checks; killed"
		}
		message := fmt.Sprintf("%s (%s); restart in %s", reason, exitStatus(err), backoff)
		if tail := tailLines(stderrPath, 3); tail != "" {
			message += "; stderr: " + tail
		}
		supervisorLog(dir, message)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, 15*time.Second)
	}
}

// treeJob складывает движок в задание надзирателя.
//
// Предела «гасить при закрытии» у задания НЕТ нарочно: надзирателя
// останавливают вместе с задачей планировщика, и такой предел обрывал бы запись,
// которую человек ведёт прямо сейчас (случай 11.11.2026). Задание нужно для
// другого — погасить зависший движок вместе с его распознавателями.
func treeJob(pid int, dir string) windows.Handle {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		supervisorLog(dir, "daemon tree is not watched: "+err.Error())
		return 0
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		supervisorLog(dir, "daemon tree is not watched: "+err.Error())
		_ = windows.CloseHandle(job)
		return 0
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err = windows.AssignProcessToJobObject(job, process); err != nil {
		supervisorLog(dir, "daemon tree is not watched: "+err.Error())
		_ = windows.CloseHandle(job)
		return 0
	}
	return job
}

// closeTree отпускает задание. Движок при этом не гаснет: предела «гасить при
// закрытии» у задания нет.
func closeTree(job windows.Handle) {
	if job != 0 {
		_ = windows.CloseHandle(job)
	}
}

// startDaemon запускает движок отдельно от задания планировщика.
//
// Остановка задачи «Dictation Service» убивает процессы её задания — вместе с
// ними оборвалась бы и запись, которую человек ведёт прямо сейчас (случай
// 11.11.2026). CREATE_BREAKAWAY_FROM_JOB выводит движок из задания, если оно это
// разрешает; не разрешает — запускаем как раньше: работа важнее отделения.
//
// Своего задания надзиратель больше не заводит: прежде оно гасило движок при
// любом уходе надзирателя, и остановка задачи обрывала разговор. Движок и без
// задания один на человека — у него свой замок на имя пользователя.
func startDaemon(executable string, args []string, stderrPath string) (*exec.Cmd, *os.File, error) {
	stderr := openSupervisorStderr(stderrPath)
	start := func(breakaway bool) (*exec.Cmd, error) {
		command := exec.Command(executable, args...)
		proc.Hide(command) // mybits/proc
		if breakaway {
			command.SysProcAttr.CreationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB
		}
		if stderr != nil {
			command.Stderr = stderr
		}
		return command, command.Start()
	}
	command, err := start(true)
	if err == nil {
		return command, stderr, nil
	}
	supervisorLog(filepath.Dir(stderrPath), "daemon keeps the task job: "+err.Error())
	command, err = start(false)
	if err != nil {
		if stderr != nil {
			_ = stderr.Close()
		}
		return nil, nil, err
	}
	return command, stderr, nil
}

// daemonOwner — чужой живой движок, занявший общий endpoint.json.
//
// Служба на машину одна, а надзирателей бывает двое: задачу в сеансе 0 заводит
// установщик, а вход человека поднимает свой из автозапуска. Мьютекс у них
// сеансовый (`Local\`), друг друга они не видят, а endpoint.json общий — и
// выходило так, что каждый считал своим только своего движка, чужого объявлял
// мёртвым и убивал. На ноутбуке это дало 617 перезапусков из 618 запусков:
// служба жила двадцать секунд, и диктовка длиннее нескольких секунд теряла
// разговор, оставляя человеку обрывок записи.
//
// Чужой движок считается живым по самой Windows, а не по ответу: ответить он
// может и не успеть — движок занят разбором и на здоровье отвечает медленно.
func daemonOwner(media string, mine int) (int, bool) {
	data, err := os.ReadFile(filepath.Join(media, "endpoint.json"))
	if err != nil {
		return 0, false
	}
	var endpoint Endpoint
	if json.Unmarshal(data, &endpoint) != nil || endpoint.PID == 0 || endpoint.PID == mine {
		return 0, false
	}
	if !processAlive(endpoint.PID) {
		return 0, false
	}
	return endpoint.PID, true
}

// senior — чужой движок старше моего.
//
// Сеансы спрашиваем у самой Windows: по имени пользователя их не различить —
// оба надзирателя работают под одним человеком.
func senior(other, mine int) bool {
	чужой, err := sessionOf(other)
	if err != nil {
		return false
	}
	мой, err := sessionOf(mine)
	if err != nil {
		return false
	}
	return seniorSessions(чужой, мой, other, mine)
}

func sessionOf(pid int) (uint32, error) {
	var сеанс uint32
	if err := windows.ProcessIdToSessionId(uint32(pid), &сеанс); err != nil {
		return 0, err
	}
	return сеанс, nil
}

// seniorSessions — правило старшинства само по себе.
//
// Сеанс человека старше служебного: в сеансе 0 нет ни микрофона, ни лотка, ни
// клавиатуры, и диктовка там бесполезна. Внутри одного сеанса старший тот, кто
// завёлся раньше: номер процесса растёт, и решение выходит одинаковым с обеих
// сторон — иначе уступили бы оба и службы не осталось бы вовсе.
func seniorSessions(чужойСеанс, мойСеанс uint32, чужойPid, мойPid int) bool {
	if чужойСеанс != мойСеанс {
		if мойСеанс == 0 {
			return true
		}
		if чужойСеанс == 0 {
			return false
		}
	}
	return чужойPid < мойPid
}

// processAlive — жив ли процесс. Отказ в доступе тоже означает «жив»: чужой
// процесс в другом сеансе открыть не дадут, а мёртвым он от этого не станет.
func processAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return false
	}
	return status == uint32(windows.WAIT_TIMEOUT)
}

func daemonHealthy(ctx context.Context, dir string, pid int) bool {
	data, err := os.ReadFile(filepath.Join(dir, "endpoint.json"))
	if err != nil {
		return false
	}
	var endpoint Endpoint
	if json.Unmarshal(data, &endpoint) != nil || endpoint.PID != pid {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", endpoint.URL+"/health", nil)
	if err != nil {
		return false
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode == http.StatusOK
}
func supervisorLog(dir, message string) {
	path := filepath.Join(dir, "supervisor.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 1<<20 {
		_ = os.Remove(path + ".old")
		_ = os.Rename(path, path+".old")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err == nil {
		_, _ = fmt.Fprintln(file, time.Now().UTC().Format(time.RFC3339), message)
		_ = file.Close()
	}
}

// openSupervisorStderr открывает файл потока ошибок службы, ограничивая его
// размер: журнал не должен расти без предела. Старый откладывается рядом.
func openSupervisorStderr(path string) *os.File {
	const limit = 256 << 10
	if info, err := os.Stat(path); err == nil && info.Size() > limit {
		_ = os.Remove(path + ".old")
		_ = os.Rename(path, path+".old")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil
	}
	return file
}

// exitStatus — код возврата службы для журнала надзирателя.
func exitStatus(err error) string {
	if err == nil {
		return "exit=0"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("exit=%d", exit.ExitCode())
	}
	return "exit=unknown (" + err.Error() + ")"
}

// tailLines — последние n непустых строк файла, одной строкой для журнала.
func tailLines(path string, n int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	const read = 64 << 10
	if info, err := file.Stat(); err == nil && info.Size() > read {
		_, _ = file.Seek(-read, io.SeekEnd)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	kept := make([]string, 0, n)
	for at := len(lines) - 1; at >= 0 && len(kept) < n; at-- {
		line := strings.TrimSpace(lines[at])
		if line == "" || strings.HasPrefix(line, "--- started ") {
			continue
		}
		kept = append([]string{line}, kept...)
	}
	return strings.Join(kept, " | ")
}
