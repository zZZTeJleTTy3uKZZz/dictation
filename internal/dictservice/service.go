// Package dictservice — диктовка как настоящая служба Windows.
//
// Зачем служба, а не задача планировщика: задача живёт в сеансе человека и
// умирает вместе с ним, а в списке служб её нет вовсе — человек не видит, что
// диктовка вообще установлена. Служба поднимается до входа в систему, её видно
// в диспетчере задач, и она переживает выход человека из сеанса.
//
// Но у службы есть жёсткое ограничение Windows: она живёт в нулевом сеансе и
// рабочего стола человека не видит. Значит печатать в поле, где стоит курсор,
// записывать с микрофона и держать значок в лотке она не может — это умеет
// только программа в сеансе человека. Поэтому половины две:
//
//	служба (нулевой сеанс, LocalSystem)  — поднимает сеансовую часть и следит,
//	                                       чтобы та работала;
//	сеансовая часть (сеанс человека)     — микрофон, сочетание клавиш, значок
//	                                       в лотке, вставка текста в поле.
//
// Служба запускает сеансовую часть не «как получится», а от имени вошедшего
// человека и на его рабочем столе: иначе значок не появится в лотке, а
// сочетание клавиш не сработает.
package dictservice

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/localdict"
)

// Имя службы в системе. Оно же — то, что человек видит в списке служб.
const Name = "Dictation"

// DisplayName и Description — то, что читает человек в диспетчере задач.
const (
	DisplayName = "Диктовка MeetDigest"
	Description = "Диктовка: приём звука, распознавание речи и вставка текста. " +
		"Служба поднимает сеансовую часть — ту, у которой есть доступ к рабочему столу, " +
		"микрофону и сочетанию клавиш."
)

// Флаги и значения, которые нужны CreateProcessAsUser. Своими именами, а не из
// x/sys: там они есть не во всех версиях, а зависеть от версии чужого пакета
// ради четырёх чисел не хочется.
const (
	createUnicodeEnvironment = 0x00000400
	createNewProcessGroup    = 0x00000200
	startfUseShowWindow      = 0x00000001
	swHide                   = 0
	maximumAllowed           = 0x02000000
	securityImpersonation    = 2
	tokenPrimary             = 1
	// Никто не вошёл в систему: так Windows отвечает вместо номера сеанса.
	noSession = 0xFFFFFFFF
	// stopCheckpointEvery — как часто служба говорит Windows «останавливаюсь,
	// подожди ещё». Без отметок SCM решает, что служба зависла, и обрывает
	// остановку через полминуты — а разговор может идти ещё десять.
	stopCheckpointEvery = 10 * time.Second
	// agentStopWait — сколько служба ждёт ответа сеансовой части сверх её
	// собственного предела: та сама решает, когда отказать.
	agentStopWait = time.Minute
	// shutdownAgentWait — сколько ждёт служба при выключении машины. Windows
	// даёт службе секунды, а не минуты, и разбор там не ждётся вовсе.
	shutdownAgentWait = 20 * time.Second
	// agentStopRefused — тот же код, что у dictation.exe --quit: идёт запись,
	// остановка отложена.
	agentStopRefused = localdict.ExitRecording
)

// agentArgs — с чем поднимается сеансовая часть. Это тот же самый файл: он сам
// решает, кем быть, по своему ключу.
var agentArgs = []string{"--supervise"}

// Install ставит службу и убирает прежние способы запуска, чтобы диктовка
// поднималась ровно одним способом, а не тремя сразу.
//
// Ставить службу может только человек с правами администратора: Windows не
// отдаёт управление службами никому другому. Поэтому установщик зовёт это от
// себя, а не служба сама себя.
func Install(exe string) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("нет доступа к службам Windows (нужны права администратора): %w", err)
	}
	defer manager.Disconnect()

	// ServiceType и BinaryPathName обязательны при обновлении уже стоящей
	// службы: UpdateConfig не подставляет умолчания, как CreateService, и с
	// нулевым типом Windows отвечает «The parameter is incorrect», а без пути
	// служба остаётся запускать прежний файл после переезда программы.
	config := mgr.Config{
		ServiceType:    windows.SERVICE_WIN32_OWN_PROCESS,
		BinaryPathName: syscall.EscapeArg(exe) + " --service",
		DisplayName:    DisplayName,
		Description:    Description,
		StartType:      mgr.StartAutomatic,
		ErrorControl:   mgr.ErrorNormal,
	}
	service, err := manager.OpenService(Name)
	if err == nil {
		// Служба уже стоит: обновляем описание и путь — файл мог переехать.
		defer service.Close()
		if err = service.UpdateConfig(config); err != nil {
			return fmt.Errorf("служба не перенастроена: %w", err)
		}
	} else {
		service, err = manager.CreateService(Name, exe, config, "--service")
		if err != nil {
			return fmt.Errorf("служба не создана: %w", err)
		}
		defer service.Close()
	}
	// Падение службы не должно оставаться незамеченным: три попытки поднять
	// заново, потом Windows перестаёт пытаться и говорит об этом в журнале.
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	_ = service.SetRecoveryActions(actions, 3600)

	// Сперва убираем прежние способы запуска, и только потом поднимаем службу.
	// Наоборот не годится: старая сеансовая часть ещё держит свой замок, служба
	// поднимает вторую, та молча уходит, и диктовка остаётся без присмотра
	// ровно до следующей попытки.
	removeOldAutostart()
	if err = service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("служба не запущена: %w", err)
	}
	return nil
}

// Uninstall снимает службу. Прежние способы запуска при этом не возвращаются:
// человек, снимающий службу, хочет тишины, а не другого способа запуска.
func Uninstall() error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("нет доступа к службам Windows (нужны права администратора): %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(Name)
	if err != nil {
		return nil // её и нет — работа сделана
	}
	defer service.Close()
	_, _ = service.Control(svc.Stop)
	for i := 0; i < 20; i++ {
		status, err := service.Query()
		if err != nil || status.State == svc.Stopped {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err = service.Delete(); err != nil {
		return fmt.Errorf("служба не снята: %w", err)
	}
	return nil
}

// Run работает службой до остановки. Зовётся Windows, а не человеком.
func Run(exe string) error {
	return svc.Run(Name, &handler{exe: exe})
}

// removeOldAutostart убирает то, чем диктовка поднималась до службы: запись в
// автозапуске (человека и общую) и задачи планировщика. Оставить их — значит
// поднимать диктовку двумя способами сразу и однажды получить две копии,
// дерущиеся за микрофон и сочетание клавиш. Общая запись HKLM появляется, когда
// установщик ставит «для всех», а службу поставить не удалось: он честно
// оставляет запуск в сеансе, а служба, вставшая позже, обязана её снять.
func removeOldAutostart() {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		if key, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE); err == nil {
			_ = key.DeleteValue("Dictation")
			_ = key.Close()
		}
	}
	for _, task := range []string{`\Dictation Service`, `\Dictation Tray`} {
		// Сперва останавливаем: удаление задачи не гасит уже запущенный её
		// экземпляр, и диктовка осталась бы работать сама по себе, без службы.
		stop := exec.Command("schtasks.exe", "/end", "/tn", task)
		stop.SysProcAttr = hiddenProcess()
		_ = stop.Run()
		remove := exec.Command("schtasks.exe", "/delete", "/tn", task, "/f")
		remove.SysProcAttr = hiddenProcess()
		_ = remove.Run()
	}
}

// handler — служба изнутри: она держит сеансовую часть живой и поднимает
// узкого помощника вставки, только когда у пользователя есть повышенный токен.
type handler struct {
	exe  string
	proc windows.Handle
	// next — раньше этого времени сеансовую часть не трогаем: она уже работает
	// сама, и дёргать её каждые пять секунд незачем.
	next time.Time
	// pasterProc, paster.Session и pasterNext — тот же присмотр за помощником.
	pasterProc windows.Handle
	paster     uint32
	pasterNext time.Time
	// stopping — службу просят остановиться. Пока это так, сеансовую часть
	// заново не поднимаем: она заняла бы файлы ровно перед заменой.
	stopping atomic.Bool
}

// Execute — точка, в которую Windows присылает команды службе.
//
// Остановка службы не рвёт разговор. Сначала сеансовую часть
// просят выйти по-хорошему: она доводит идущую сессию до конца и только потом
// уходит. Не дождались — служба отказывает в остановке и остаётся работать;
// сеансовая часть при этом продолжает запись, а не обрывает её.
func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	go h.watch()
	// PRESHUTDOWN приходит раньше STOP и SHUTDOWN: Windows даёт службе время
	// закрыть свои дела до того, как гасить всё остальное.
	accepted := svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPreShutdown
	status <- svc.Status{State: svc.Running, Accepts: accepted}
	stopped := make(chan uint32, 1)
	ticker := time.NewTicker(stopCheckpointEvery)
	defer ticker.Stop()
	stopping := false
	checkpoint := 0
	for {
		select {
		case request, ok := <-requests:
			if !ok {
				return false, 0
			}
			switch request.Cmd {
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.PreShutdown, svc.Shutdown:
				// Выключение машины: ждать конца разговора некогда. Сеансовую
				// часть просят закрыть запись и выйти — это доли секунды, и
				// звук при этом не теряется: файл и журнал закрываются.
				status <- svc.Status{State: svc.StopPending}
				h.stopping.Store(true)
				h.stopPaster("shutdown")
				h.closeSession()
				return false, 0
			case svc.Stop:
				if stopping {
					continue
				}
				stopping = true
				checkpoint = 1
				h.stopping.Store(true)
				h.stopPaster("stop")
				status <- svc.Status{State: svc.StopPending, CheckPoint: 1, WaitHint: uint32(stopCheckpointEvery/time.Second) + 30}
				go func() { stopped <- h.stopSession() }()
			}
		case code := <-stopped:
			if code == 0 {
				return false, 0
			}
			// Отказ: сессия не кончилась. Служба остаётся работать вместе с
			// записью — рвать разговор ради остановки службы нельзя. Файлы
			// при этом заняты, и установщик со скриптом выкатки узнают об этом
			// из --status, а не из ошибки копирования.
			h.logf("остановка отложена и отказана: идёт запись, сеансовая часть продолжает работать")
			stopping = false
			h.stopping.Store(false)
			status <- svc.Status{State: svc.Running, Accepts: accepted}
		case <-ticker.C:
			if !stopping {
				continue
			}
			checkpoint++
			status <- svc.Status{State: svc.StopPending, CheckPoint: uint32(checkpoint), WaitHint: uint32(stopCheckpointEvery/time.Second) + 30}
		}
	}
}

// stopSession просит сеансовую часть выйти и возвращает её код возврата.
//
// Ждёт помощник, а не служба: у помощника те же правила, что у установщика —
// он ждёт конца записи, закрывает файл и уходит. Службе остаётся код: ноль —
// диктовки больше нет, agentStopRefused — идёт запись.
func (h *handler) stopSession() uint32 {
	wait := localdict.StopWaitSetting(localdict.SettingsDir())
	code, err := h.askAgent([]string{"--quit", "--wait", strconv.Itoa(int(wait / time.Second))}, wait+agentStopWait)
	if err != nil {
		h.logf("сеансовая часть не ответила на остановку: " + err.Error())
		return agentStopRefused
	}
	if code != 0 {
		h.logf(fmt.Sprintf("сеансовая часть отказала в остановке: код=%d предел=%s", code, wait))
	}
	return code
}

// closeSession закрывает запись при выключении машины: без ожидания разбора.
func (h *handler) closeSession() {
	code, err := h.askAgent([]string{"--quit", "--flush", "--wait", "5"}, shutdownAgentWait)
	if err != nil {
		h.logf("запись при выключении не закрыта: " + err.Error())
		return
	}
	h.logf(fmt.Sprintf("запись при выключении закрыта, код=%d", code))
}

// askAgent запускает команду диктовки в сеансе человека и ждёт её ответа.
// Возвращает код возврата: им программа и говорит, можно ли её останавливать.
func (h *handler) askAgent(args []string, timeout time.Duration) (uint32, error) {
	session := windows.WTSGetActiveConsoleSessionId()
	if session == noSession {
		return 0, errors.New("никто не вошёл в систему: останавливать нечего")
	}
	handle, err := startInSession(session, h.exe, args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	state, err := windows.WaitForSingleObject(handle, uint32(timeout/time.Millisecond))
	if err != nil {
		return 0, err
	}
	if state != uint32(windows.WAIT_OBJECT_0) {
		return 0, fmt.Errorf("диктовка не ответила за %s", timeout)
	}
	var code uint32
	if err = windows.GetExitCodeProcess(handle, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// logf пишет строку в журнал службы рядом с настройками: он читается и без
// прав администратора, в отличие от журнала событий Windows.
func (h *handler) logf(message string) { logService(message) }

// serviceLogf — тот же журнал, но как шов для проверок выбора ключа: им не
// надо писать в настоящий каталог настроек.
var serviceLogf = logService

func logService(message string) {
	dir := localdict.SettingsDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	path := filepath.Join(dir, "service.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 256<<10 {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	line := time.Now().Format(time.RFC3339) + " " + message + "\n"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	_, _ = file.WriteString(line)
	_ = file.Close()
}

// watch следит за сеансовой частью, пока служба работает.
func (h *handler) watch() {
	for {
		if !h.stopping.Load() {
			h.ensureAgent()
			h.ensurePaster()
		}
		time.Sleep(5 * time.Second)
	}
}

// ensureAgent поднимает сеансовую часть, если её нет.
//
// Живость проверяется по самому процессу, а не по файлам и не по порту: тот,
// кого мы подняли, — единственный, о ком служба знает точно. Процесс закрылся
// (человек вышел, диктовка упала, её сняли силой) — поднимаем заново.
func (h *handler) ensureAgent() {
	if h.stopping.Load() || time.Now().Before(h.next) {
		return
	}
	if h.proc != 0 {
		state, err := windows.WaitForSingleObject(h.proc, 0)
		if err == nil && state == windows.WAIT_OBJECT_0 {
			_ = windows.CloseHandle(h.proc)
			h.proc = 0
		} else {
			return // жив
		}
	}
	session := windows.WTSGetActiveConsoleSessionId()
	if session == noSession {
		return // никто не вошёл в систему: поднимать некого
	}
	handle, err := startInSession(session, h.exe, agentArgs...)
	if err != nil {
		return // войдём сюда же через пять секунд
	}
	// Вышел сразу — значит сеансовая часть уже работает: у неё свой замок на
	// имя человека, и второй такой же процесс молча уходит. Тогда ждём минуту,
	// а не поднимаем её поминутно.
	if state, waitErr := windows.WaitForSingleObject(handle, 1500); waitErr == nil && state == windows.WAIT_OBJECT_0 {
		_ = windows.CloseHandle(handle)
		h.next = time.Now().Add(time.Minute)
		return
	}
	h.proc = handle
}

// tokenLinked — связанный токен из GetTokenInformation(TokenLinkedToken=19).
// У администратора с включённым UAC он указывает на повышенный токен; у
// обычного человека связанного токена нет. Шов нужен проверке выбора ключа.
var tokenLinked = func(token windows.Token) (windows.Token, error) { return token.GetLinkedToken() }

// duplicatePrimary — DuplicateTokenEx в первичный токен для CreateProcessAsUser.
var duplicatePrimary = func(token windows.Token) (windows.Token, error) {
	var primary windows.Token
	err := windows.DuplicateTokenEx(token, maximumAllowed, nil, securityImpersonation, tokenPrimary, &primary)
	return primary, err
}

// errNoLinkedToken — человек не администратор или UAC выключен: повышенного
// помощника вставки быть не должно, и это не ошибка службы.
var errNoLinkedToken = errors.New("у пользователя нет связанного повышенного токена")

// elevatedUserToken возвращает дубликат связанного повышенного токена только
// для помощника вставки. Сеансовая часть целиком остаётся среднего уровня:
// повышать её нельзя — через её локальный HTTP API любой процесс пользователя
// заставил бы High IL слать ввод в администраторские окна и ставить обновления.
func elevatedUserToken(user windows.Token) (windows.Token, error) {
	linked, err := tokenLinked(user)
	if err != nil {
		return 0, err
	}
	if linked == 0 {
		return 0, errNoLinkedToken
	}
	defer func() { _ = linked.Close() }()
	primary, err := duplicatePrimary(linked)
	if err != nil {
		return 0, fmt.Errorf("повышенный ключ не продублирован: %w", err)
	}
	return primary, nil
}

// startInSession запускает программу от имени вошедшего человека и на его
// рабочем столе. Без этого значок не появится в лотке, сочетание клавиш не
// сработает, а микрофон будет недоступен: у нулевого сеанса ничего этого нет.
func startInSession(session uint32, exe string, args ...string) (windows.Handle, error) {
	var token windows.Token
	if err := windows.WTSQueryUserToken(session, &token); err != nil {
		return 0, fmt.Errorf("не получен ключ доступа вошедшего человека: %w", err)
	}
	defer func() { _ = token.Close() }()

	// Сеансовая часть идёт с обычным отфильтрованным токеном, как и до службы.
	// Повышение нужно только узкому помощнику вставки — см. paster.go.
	var primary windows.Token
	if err := windows.DuplicateTokenEx(token, maximumAllowed, nil, securityImpersonation, tokenPrimary, &primary); err != nil {
		return 0, fmt.Errorf("ключ доступа не продублирован: %w", err)
	}
	defer func() { _ = primary.Close() }()
	serviceLogf("event=session-token elevated=false")
	return startWithToken(session, exe, primary, args...)
}

// startWithToken запускает программу в сеансе с уже выбранным первичным
// токеном. Для сеансовой части он обычный, для помощника вставки — повышенный.
func startWithToken(session uint32, exe string, primary windows.Token, args ...string) (windows.Handle, error) {
	// Окружение берём у человека, а не у службы: иначе диктовка искала бы свои
	// настройки в служебном профиле и не нашла бы ни настроек, ни моделей.
	var environment *uint16
	if err := windows.CreateEnvironmentBlock(&environment, primary, false); err != nil {
		environment = nil
	}
	if environment != nil {
		defer func() { _ = windows.DestroyEnvironmentBlock(environment) }()
	}

	commandLine, err := windows.UTF16PtrFromString(`"` + exe + `" ` + joinArgs(args))
	if err != nil {
		return 0, err
	}
	directory, err := windows.UTF16PtrFromString(filepath.Dir(exe))
	if err != nil {
		return 0, err
	}
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)

	var startup windows.StartupInfo
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Desktop = desktop
	// Окно не показываем: у сеансовой части его быть не должно вовсе, а если
	// она когда-нибудь его заведёт, оно не должно мелькнуть на экране.
	startup.Flags = startfUseShowWindow
	startup.ShowWindow = swHide

	var process windows.ProcessInformation
	flags := uint32(createUnicodeEnvironment | createNewProcessGroup)
	if err = windows.CreateProcessAsUser(primary, nil, commandLine, nil, nil, false, flags, environment, directory, &startup, &process); err != nil {
		return 0, fmt.Errorf("сеансовая часть не запущена: %w", err)
	}
	_ = windows.CloseHandle(process.Thread)
	return process.Process, nil
}

// joinArgs склеивает ключи в строку запуска. Кавычки ставим каждому: путь с
// пробелами Windows иначе разрежет на два ключа.
func joinArgs(args []string) string {
	line := ""
	for _, arg := range args {
		if line != "" {
			line += " "
		}
		if len(arg) > 0 && arg[0] == '-' {
			line += arg
			continue
		}
		line += `"` + arg + `"`
	}
	return line
}

// hiddenProcess — запуск без окна: CREATE_NO_WINDOW. HideWindow один не годится,
// он прячет уже созданное окно, и на быстрой машине оно успевает мелькнуть.
func hiddenProcess() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// Executable — путь к самой программе: и служба, и сеансовая часть — один файл.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Clean(exe), nil
}
