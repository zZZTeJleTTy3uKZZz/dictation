// Package dictpaster — узкий помощник вставки с повышенными правами.
//
// Его единственная команда — нажать Ctrl+V в указанном окне. Ни HTTP, ни
// настроек, ни ключей, ни запуска процессов: если повысить всю сеансовую
// часть, любой процесс пользователя достанется до её локального API и
// заставит её слать ввод в администраторские окна и ставить обновления.
// Здесь повышение есть только у этой программы и только на время нажатия.
package dictpaster

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/appdirs"
)

var user32 = syscall.NewLazyDLL("user32.dll")
var kernel32 = syscall.NewLazyDLL("kernel32.dll")

const (
	maxCommandBytes = 256
	// maxTextChars — предел буфера для вставки в администраторское окно.
	// Большая фраза в терминале не должна превратиться в длинную команду.
	maxTextChars = 65536
)

// Швы для проверок: на машине Windows тест подменяет границы, не нажимая
// настоящие клавиши и не открывая чужое окно.
var (
	clientImagePath = imagePathOfProcess
	clientSession   = sessionOfProcess
	readClipboard   = readClipboardText
	foreground      = foregroundWindow
	keysReleased    = modifierKeysReleased
	pasteKeys       = sendCtrlV
	ownSession      = currentSession
	writeLog        = pasterLog
)

// PipeName — имя канала текущего сеанса. Сеансовые части и помощник живут в
// одном сеансе, поэтому другие программы пользователя видят имя, но не могут
// подключиться: DACL пускает только SID пользователя и SYSTEM.
func PipeName() (string, error) {
	session, err := ownSession()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`\\.\pipe\meetdigest-paster-%d`, session), nil
}

func currentSession() (uint32, error) {
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		return 0, err
	}
	return session, nil
}

func imagePathOfProcess(pid uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var name [32768]uint16
	size := uint32(len(name))
	if err := windows.QueryFullProcessImageName(process, 0, &name[0], &size); err != nil {
		return "", err
	}
	return syscall.UTF16ToString(name[:size]), nil
}

func sessionOfProcess(pid uint32) (uint32, error) {
	var session uint32
	if err := windows.ProcessIdToSessionId(pid, &session); err != nil {
		return 0, err
	}
	return session, nil
}

func foregroundWindow() uintptr {
	window, _, _ := user32.NewProc("GetForegroundWindow").Call()
	return window
}

func modifierKeysReleased() bool {
	for _, key := range []uintptr{0x10, 0x11, 0x12, 0x5b, 0x5c} {
		state, _, _ := user32.NewProc("GetAsyncKeyState").Call(key)
		if state&0x8000 != 0 {
			return false
		}
	}
	return true
}

type keyInput struct {
	Kind        uint32
	_           uint32
	VK, Scan    uint16
	Flags, Time uint32
	_           uint32
	Extra       uintptr
	_           uint64
}

func sendCtrlV() bool {
	inputs := []keyInput{{Kind: 1, VK: 0x11}, {Kind: 1, VK: 0x56}, {Kind: 1, VK: 0x56, Flags: 2}, {Kind: 1, VK: 0x11, Flags: 2}}
	count, _, _ := user32.NewProc("SendInput").Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(inputs[0]))
	return count == uintptr(len(inputs))
}

// readClipboardText отдаёт текст буфера, не меняя его. Пустой буфер и
// перевод строки — отказ: в администраторской консоли Enter выполнил бы
// команду, а не перенёс бы строку.
func readClipboardText() (string, error) {
	ok, _, _ := user32.NewProc("OpenClipboard").Call(0)
	if ok == 0 {
		return "", errors.New("clipboard is busy")
	}
	defer func() { _, _, _ = user32.NewProc("CloseClipboard").Call() }()
	data, _, _ := user32.NewProc("GetClipboardData").Call(13) // CF_UNICODETEXT
	if data == 0 {
		return "", errors.New("no text in clipboard")
	}
	pointer, _, _ := kernel32.NewProc("GlobalLock").Call(data)
	if pointer == 0 {
		return "", errors.New("clipboard lock failed")
	}
	defer func() { _, _, _ = kernel32.NewProc("GlobalUnlock").Call(data) }()
	size, _, _ := kernel32.NewProc("GlobalSize").Call(data)
	if size == 0 || size > uintptr(maxTextChars*2+2) {
		return "", errors.New("clipboard text is too large")
	}
	buffer := make([]byte, size)
	// uintptr от GlobalLock нельзя превращать в unsafe.Pointer: проверяющий
	// указатели анализатор справедливо ругается. Копируем через RtlMoveMemory,
	// как это делает Clipboard в localdict.
	_, _, _ = kernel32.NewProc("RtlMoveMemory").Call(uintptr(unsafe.Pointer(&buffer[0])), pointer, size)
	units := make([]uint16, size/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(buffer[i*2:])
	}
	return syscall.UTF16ToString(units), nil
}

// clipboardTextSafe — строка годна для вставки в окно администратора.
func clipboardTextSafe(text string) bool {
	if text == "" || len([]rune(text)) > maxTextChars {
		return false
	}
	return !strings.ContainsAny(text, "\r\n\u2028\u2029")
}

// authorizedClient пускает только тот же dictation.exe из каталога установки
// и из того же сеанса. Путь сравнивается целиком, а не по имени файла.
func authorizedClient(pid, session uint32, expectedExe string) bool {
	path, err := clientImagePath(pid)
	if err != nil {
		return false
	}
	if !strings.EqualFold(filepath.Clean(path), expectedExe) {
		return false
	}
	client, err := clientSession(pid)
	return err == nil && client == session
}

// Serve поднимает единственный экземпляр канала и отвечает на команды, пока
// процесс жив. Возврат — причина выхода; служба поднимет помощника заново.
func Serve() error {
	name, err := PipeName()
	if err != nil {
		return fmt.Errorf("имя канала помощника: %w", err)
	}
	session, err := currentSession()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	expectedExe := filepath.Clean(exe)

	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("SID пользователя помощника: %w", err)
	}
	sddl := "D:P(A;;GA;;;" + user.User.Sid.String() + ")(A;;GA;;;SY)"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("DACL канала помощника: %w", err)
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	writeLog("paster-start", "session="+strconv.FormatUint(uint64(session), 10))
	for {
		pipe, err := windows.CreateNamedPipe(path, windows.PIPE_ACCESS_DUPLEX,
			windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT,
			1, maxCommandBytes, maxCommandBytes, 0, attributes)
		if err != nil {
			writeLog("paster-pipe-failed", "err="+err.Error())
			return err
		}
		if err = windows.ConnectNamedPipe(pipe, nil); err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			writeLog("paster-connect-failed", "err="+err.Error())
			_ = windows.CloseHandle(pipe)
			continue
		}
		serveClient(pipe, session, expectedExe)
		_ = windows.DisconnectNamedPipe(pipe)
		_ = windows.CloseHandle(pipe)
	}
}

func serveClient(pipe windows.Handle, session uint32, expectedExe string) {
	var pid uint32
	answer := "refused bad-client"
	if err := windows.GetNamedPipeClientProcessId(pipe, &pid); err == nil && authorizedClient(pid, session, expectedExe) {
		answer = serveCommand(pipe)
	}
	if err := windows.WriteFile(pipe, []byte(answer), nil, nil); err != nil {
		writeLog("paster-reply-failed", "err="+err.Error())
	}
	writeLog("paster-request", "answer="+strings.Fields(answer)[0])
}

func serveCommand(pipe windows.Handle) string {
	buffer := make([]byte, maxCommandBytes)
	var received uint32
	if err := windows.ReadFile(pipe, buffer, &received, nil); err != nil || received == 0 {
		return "refused bad-request"
	}
	command := strings.TrimSpace(string(buffer[:received]))
	windowText, ok := strings.CutPrefix(command, "PASTE ")
	if !ok {
		return "refused bad-command"
	}
	window, err := strconv.ParseUint(strings.TrimSpace(windowText), 10, 64)
	if err != nil || window == 0 {
		return "refused bad-window"
	}
	switch reason := pasteIntoWindow(uintptr(window)); reason {
	case "":
		return "ok"
	default:
		return "refused " + reason
	}
}

// pasteIntoWindow — единственное действие помощника. Возвращает пустую строку
// при удаче или причину отказа (без содержимого буфера).
func pasteIntoWindow(window uintptr) string {
	if window != foreground() {
		return "focus-moved"
	}
	if !keysReleased() {
		return "keys-held"
	}
	text, err := readClipboard()
	if err != nil {
		return "clipboard-unreadable"
	}
	if !clipboardTextSafe(text) {
		return "unsafe-text"
	}
	if !pasteKeys() {
		return "blocked"
	}
	return ""
}

// pasterLog пишет короткие строки без текста фразы: журнал помощника должен
// помогать разбирать отказы, а не становиться копией диктовок.
func pasterLog(event, meta string) {
	dir := appdirs.SettingsDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	path := filepath.Join(dir, "paster.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 256<<10 {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(file, "%s event=%s %s\n", time.Now().Format(time.RFC3339Nano), event, meta)
	_ = file.Close()
}
