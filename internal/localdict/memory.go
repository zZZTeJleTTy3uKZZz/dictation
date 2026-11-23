package localdict

// Управление памятью в простое.
//
// ЗАЧЕМ. Диктовка — виджет: «записал → отправил → вставил». В простое она не
// делает ничего, а на машине владельца надзиратель и служба занимали 12 + 32
// МБ рабочего набора. Часть этого — не наш код, а
// страницы библиотек, тронутые при запуске, и куча Go, которую сборщик мусора
// не спешит отдавать. Отдать их системе можно двумя ручками:
//
//   - debug.FreeOSMemory собирает мусор и возвращает освободившееся системе;
//   - SetProcessWorkingSetSizeEx(-1, -1) выталкивает рабочий набор, и Windows
//     отдаёт страницы, которые давно не трогали. Данные с диска не теряются:
//     следующий доступ прочитает их обратно.
//
// И третий рычаг — мягкий предел кучи (debug.SetMemoryLimit). Он не запрещает
// занять больше: он заставляет сборщик работать чаще, чтобы служба не росла в
// простое. Поэтому в записи предел снимается: разговор в час — это сто
// мегабайт звука в памяти, и душить его пределом 12 МиБ значило бы жечь
// процессор на каждой фразе (см. releaseMemoryPressure).

import (
	"context"
	"errors"
	"math"
	"runtime/debug"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// idleMemoryLimit — мягкий предел кучи службы в простое. Подобран с запасом:
	// в простое живая куча службы — единицы мегабайт, а запас нужен на кадр
	// состояния с историей (250 КБ) и на ответ окну.
	idleMemoryLimit = 12 << 20
	// supervisorMemoryLimit — предел надзирателю. Он только запускает службу и
	// раз в две секунды проверяет здоровье; больше ему не нужно ничего.
	supervisorMemoryLimit = 6 << 20
	// idleGCPercent и busyGCPercent — как часто собирать мусор. В простое чаще:
	// куча тогда меньше, и отдавать системе нечего; в записи — обычный темп,
	// чтобы сборка не отбирала процессор у разбора речи.
	idleGCPercent = 20
	busyGCPercent = 100
	// idleMemoryAfter — сколько тишины считать простоем. Двадцать пять секунд:
	// человек отпустил клавишу, текст уже вставлен, и ждать, пока он начнёт
	// следующую фразу, незачем.
	idleMemoryAfter = 25 * time.Second
	// idleTrimEvery — как часто повторять обрезку рабочего набора в простое.
	// Первая идёт сразу по наступлении простоя, дальше — раз в минуту: страницы
	// возвращаются сами, если служба к ним не притрагивается.
	idleTrimEvery = time.Minute
	// memoryEvery — такт проверки простоя.
	memoryEvery = 5 * time.Second
	// supervisorTrimEvery — как часто надзиратель отдаёт системе свой рабочий
	// набор. Ему нечего делать, поэтому можно часто: обрезка стоит доли
	// миллисекунды, а служба запускается раз в жизни процесса — держать её
	// страницы в памяти незачем.
	supervisorTrimEvery = 10 * time.Second
)

// Ручки вынесены в переменные: проверка должна видеть, что обрезка и предел
// были позваны, не измеряя память процесса.
var (
	setMemoryLimit   = debug.SetMemoryLimit
	setGCPercent     = debug.SetGCPercent
	trimOwnMemory    = trimCurrentWorkingSet
	trimOtherProcess = trimProcessWorkingSet
)

var (
	// K32EmptyWorkingSet есть в kernel32 начиная с Windows 7; на всякий случай
	// есть запасной путь через SetProcessWorkingSetSizeEx.
	procEmptyWorkingSet = kernel32.NewProc("K32EmptyWorkingSet")
	psapi               = syscall.NewLazyDLL("psapi.dll")
	procProcessMemory   = psapi.NewProc("GetProcessMemoryInfo")
)

// processMemoryCounters — PROCESS_MEMORY_COUNTERS из psapi.h. Нужен только
// рабочий набор, но размер структуры обязан совпадать: иначе Windows откажет.
type processMemoryCounters struct {
	Size                      uint32
	PageFaultCount            uint32
	PeakWorkingSetSize        uintptr
	WorkingSetSize            uintptr
	QuotaPeakPagedPoolUsage   uintptr
	QuotaPagedPoolUsage       uintptr
	QuotaPeakNonPagedPoolSize uintptr
	QuotaNonPagedPoolUsage    uintptr
	PagefileUsage             uintptr
	PeakPagefileUsage         uintptr
}

// trimWorkingSetHandle выталкивает рабочий набор процесса. Ошибка не беда сама
// по себе: у чужого процесса может не быть прав, и тогда мы просто ничего не
// делаем.
func trimWorkingSetHandle(handle windows.Handle) error {
	if handle == 0 {
		return errors.New("memory: пустой дескриптор процесса")
	}
	if ok, _, _ := procEmptyWorkingSet.Call(uintptr(handle)); ok != 0 {
		return nil
	}
	// Запасной путь: (SIZE_T)-1 в обоих размерах — «убрать как можно больше
	// страниц». Права те же: PROCESS_SET_QUOTA и PROCESS_QUERY_INFORMATION.
	return windows.SetProcessWorkingSetSizeEx(handle, ^uintptr(0), ^uintptr(0), 0)
}

// trimCurrentWorkingSet обрезает рабочий набор самого процесса.
func trimCurrentWorkingSet() error {
	handle, err := windows.GetCurrentProcess()
	if err != nil {
		return err
	}
	return trimWorkingSetHandle(handle)
}

// trimProcessWorkingSet обрезает рабочий набор дочернего процесса по номеру.
// Дескриптор открывается на один вызов: держать его незачем, а номер процесса
// живёт дольше.
func trimProcessWorkingSet(pid int) error {
	if pid <= 0 {
		return errors.New("memory: нет номера процесса")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	return trimWorkingSetHandle(handle)
}

// workingSetOf — рабочий набор процесса. Нужен проверкам и замерам: «стало
// меньше» должно быть видно числом, а не на слово.
func workingSetOf(handle windows.Handle) (uintptr, error) {
	var counters processMemoryCounters
	counters.Size = uint32(unsafe.Sizeof(counters))
	ok, _, err := procProcessMemory.Call(uintptr(handle), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
	if ok == 0 {
		return 0, err
	}
	return counters.WorkingSetSize, nil
}

// tuneSupervisorMemory — надзирателю маленький предел и частая сборка.
//
// Ему нечего держать: ни истории, ни звука, ни окна. Предел нужен не ради
// экономии как таковой, а чтобы будущая правка, случайно положившая в
// надзирателя что-то тяжёлое, сразу упёрлась в него на проверке.
func tuneSupervisorMemory() {
	setMemoryLimit(supervisorMemoryLimit)
	setGCPercent(idleGCPercent)
	freeOSMemory()
	_ = trimOwnMemory()
}

// releaseMemoryPressure снимает предел и возвращает обычный темп сборки перед
// записью: разговор в час — это сто мегабайт звука, и мягкий предел 12 МиБ
// заставил бы сборщик работать на каждой фразе.
func (a *App) releaseMemoryPressure() {
	a.mu.Lock()
	tuned := a.memoryTuned
	a.memoryTuned = false
	a.mu.Unlock()
	if !tuned {
		return
	}
	setMemoryLimit(math.MaxInt64)
	setGCPercent(busyGCPercent)
}

// idleMemoryCheck — одна проверка простоя. Зовётся по такту и из проверок.
func (a *App) idleMemoryCheck(now time.Time) {
	a.mu.Lock()
	idle := !a.busy && !a.idleSince.IsZero() && now.Sub(a.idleSince) >= idleMemoryAfter
	if !idle {
		a.mu.Unlock()
		return
	}
	first := !a.memoryTuned
	if !first && now.Sub(a.lastTrim) < idleTrimEvery {
		a.mu.Unlock()
		return
	}
	a.memoryTuned = true
	a.lastTrim = now
	a.mu.Unlock()

	// Предел и сборка — один раз на вход в простой. Дальше только рабочий
	// набор: страницы возвращаются сами, а полная сборка каждую минуту жгла бы
	// процессор без пользы.
	if first {
		setMemoryLimit(idleMemoryLimit)
		setGCPercent(idleGCPercent)
		freeOSMemory()
	}
	_ = trimOwnMemory()
}

// memoryLoop — такт простоя службы. Живёт до закрытия приложения.
func (a *App) memoryLoop(ctx context.Context) {
	ticker := time.NewTicker(memoryEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			a.idleMemoryCheck(now)
		}
	}
}
