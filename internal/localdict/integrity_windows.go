package localdict

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tokenMandatoryLabel — TOKEN_MANDATORY_LABEL: указатель на SID уровня
// целостности и атрибуты. x/sys не объявляет эту структуру, а GetTokenInformation
// ждёт ровно её.
type tokenMandatoryLabel struct {
	Label      *windows.SID
	Attributes uint32
}

// integrityOfToken возвращает RID из SID S-1-16-<уровень>: 0x2000 — средний,
// 0x3000 — высокий. Чем больше число, тем выше права.
func integrityOfToken(token windows.Token) (uint32, error) {
	// GetTokenInformation отдаёт структуру переменного размера: вместе с
	// TOKEN_MANDATORY_LABEL приходит и сам SID. Фиксированного буфера на
	// sizeof(tokenMandatoryLabel) мало — Windows отвечает
	// ERROR_INSUFFICIENT_BUFFER и в returned кладёт нужную длину. Первый
	// вызов с 64 байтами покрывает обычный SID, повтор — любую нехватку.
	buffer := make([]byte, 64)
	for attempt := 0; attempt < 2; attempt++ {
		var returned uint32
		err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel,
			&buffer[0], uint32(len(buffer)), &returned)
		if errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) && returned > uint32(len(buffer)) && returned <= 4096 {
			buffer = make([]byte, returned)
			continue
		}
		if err != nil {
			return 0, err
		}
		break
	}
	label := (*tokenMandatoryLabel)(unsafe.Pointer(&buffer[0]))
	if label.Label == nil {
		return 0, errors.New("в токене нет SID уровня целостности")
	}
	count := label.Label.SubAuthorityCount()
	if count == 0 {
		return 0, errors.New("SID уровня целостности пуст")
	}
	return label.Label.SubAuthority(uint32(count) - 1), nil
}

// readIntegrityLevelsWindows сравнивает уровень окна, в которое мы хотели бы
// вставить текст, со своим. Окно администратора не примет SendInput от
// процесса среднего уровня: Windows молча выбросит нажатия, и честный итог
// вставки должен быть false.
func readIntegrityLevelsWindows(window uintptr) (uint32, uint32, error) {
	if window == 0 {
		return 0, 0, errors.New("нет окна")
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(windows.HWND(window), &pid); err != nil {
		return 0, 0, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = windows.CloseHandle(process) }()

	var targetToken windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &targetToken); err != nil {
		return 0, 0, err
	}
	defer func() { _ = targetToken.Close() }()

	target, err := integrityOfToken(targetToken)
	if err != nil {
		return 0, 0, err
	}
	own, err := integrityOfToken(windows.GetCurrentProcessToken())
	if err != nil {
		return 0, 0, err
	}
	return target, own, nil
}
