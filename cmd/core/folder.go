// Выбор папки системным диалогом.
//
// ПОЧЕМУ СИСТЕМНЫЙ. В HTML выбрать папку нельзя: браузер умеет только файлы, да
// и то без настоящего пути. А вводить путь руками человек будет ошибаться — и
// узнает об ошибке через день, когда записи не уйдут.
package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// browseInfo — то, что SHBrowseForFolderW ждёт на входе.
//
// Порядок полей задан Windows; менять его нельзя, иначе диалог прочитает мусор.
type browseInfo struct {
	Owner       uintptr
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	Callback    uintptr
	Param       uintptr
	ImageIndex  int32
	_           uint32 // выравнивание
}

const (
	// bifNewStyle — диалог с деревом, полем ввода и кнопкой «Создать папку».
	// Без этого флага Windows показывает вид из девяностых.
	bifNewStyle = 0x00000040
	// bifReturnOnlyFileSystem — отсечь «Этот компьютер» и сетевое окружение:
	// писать записи туда всё равно нельзя.
	bifReturnOnlyFileSystem = 0x00000001
	// coInitApartmentThreaded — оконная модель COM, та же, в которой живёт диалог.
	coInitApartmentThreaded = 0x2
)

// pickFolder — спросить папку у человека. Пусто — отказался.
func pickFolder(title string) (string, error) {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	ole32 := syscall.NewLazyDLL("ole32.dll")
	browse := shell32.NewProc("SHBrowseForFolderW")
	pathOf := shell32.NewProc("SHGetPathFromIDListW")
	free := ole32.NewProc("CoTaskMemFree")
	start := ole32.NewProc("CoInitializeEx")
	stop := ole32.NewProc("CoUninitialize")

	// Диалог живёт в COM; поток уже мог его завести (это делает движок окна),
	// и тогда повторный вызов вернёт «уже начато» — не ошибка.
	_, _, _ = start.Call(0, coInitApartmentThreaded)
	defer func() { _, _, _ = stop.Call() }()

	caption, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return "", fmt.Errorf("заголовок диалога: %w", err)
	}
	name := make([]uint16, syscall.MAX_LONG_PATH)
	info := browseInfo{
		DisplayName: &name[0],
		Title:       caption,
		Flags:       bifNewStyle | bifReturnOnlyFileSystem,
	}

	chosen, _, _ := browse.Call(uintptr(unsafe.Pointer(&info)))
	if chosen == 0 {
		return "", nil // нажали «Отмена»
	}
	defer func() { _, _, _ = free.Call(chosen) }()

	path := make([]uint16, syscall.MAX_LONG_PATH)
	ok, _, _ := pathOf.Call(chosen, uintptr(unsafe.Pointer(&path[0])))
	if ok == 0 {
		return "", fmt.Errorf("выбранное место не папка на диске")
	}
	return syscall.UTF16ToString(path), nil
}
