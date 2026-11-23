// Запись экрана рядом со звуком — когда встречу надо не только слышать.
//
// ЗАЧЕМ ОТДЕЛЬНО ОТ ЗВУКА. Звук нужен всегда, экран — изредка: на встрече, где
// показывают систему, таблицу или документ. Писать его всегда — значит гонять
// сотни мегабайт ради разговоров, где смотреть не на что, и занимать процессор
// ровно тем, из-за чего 07.09.2026 у записи Телемоста пропал звук.
//
// ПОЧЕМУ ffmpeg, А НЕ СВОЙ ЗАХВАТ. Windows отдаёт картинку рабочего стола
// штатно (gdigrab), а ffmpeg уже нужен воркеру для сведения дорожек. Свой
// захват экрана здесь означал бы вторую зависимость ради того же самого.
package recorder

import (
	"fmt"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hidden"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

const (
	// screenFPS — сколько кадров в секунду писать с экрана.
	//
	// Пять, а не тридцать, и это не экономия ради экономии. 07.09.2026 запись
	// Телемоста молча потеряла 54 минуты звука на машине, где процессор весь
	// разговор был занят на 80–100 %: он писал экран в полном разрешении и
	// полной частоте. Показ таблицы или карточки CRM меняется раз в несколько
	// секунд — пяти кадров хватает, чтобы потом всё прочитать.
	screenFPS = "5"
	// screenPreset — скорость кодирования важнее размера: процессор нужен разговору.
	screenPreset = "ultrafast"
	screenCRF    = "30"
	// screenStopWait — сколько ждём, пока ffmpeg допишет файл.
	screenStopWait = 20 * time.Second
)

// ScreenRecorder — запись экрана отдельным процессом ffmpeg.
type ScreenRecorder struct {
	path    string
	width   string
	target  string
	log     *slog.Logger
	command *exec.Cmd
	stdin   io.WriteCloser
}

func NewScreen(path, width, target string, log *slog.Logger) *ScreenRecorder {
	return &ScreenRecorder{path: path, width: width, target: target, log: log}
}

// Start — начать запись. Не удалось — разговор всё равно пишется звуком.
func (s *ScreenRecorder) Start() bool {
	args := []string{
		"-y", "-loglevel", "error",
		"-f", "gdigrab", "-framerate", screenFPS,
	}
	args = append(args, s.area()...)
	args = append(args,
		"-i", "desktop",
		// Уменьшаем только то, что шире нужного: у маленького экрана
		// растягивать картинку незачем, а -2 держит пропорции и чётность.
		"-vf", fmt.Sprintf("scale='min(%s,iw)':-2", s.width),
		"-c:v", "libx264", "-preset", screenPreset, "-crf", screenCRF,
		"-pix_fmt", "yuv420p",
		s.path,
	)

	command := hidden.Command("ffmpeg", args...)
	// Без своего окна: воркер живёт в фоне, и консоль ffmpeg на весь экран —
	// последнее, что человек хочет увидеть посреди встречи.
	stdin, err := command.StdinPipe()
	if err != nil {
		s.log.Warn("экран записать не удалось", "err", err)
		return false
	}
	if err := command.Start(); err != nil {
		s.log.Warn("экран записать не удалось", "err", err)
		return false
	}
	s.command = command
	s.stdin = stdin
	s.log.Info("пишу и экран", "file", s.path, "экран", s.target)
	return true
}

// area — какую часть рабочего стола снимать.
func (s *ScreenRecorder) area() []string {
	if s.target == "" || s.target == "all" {
		return nil
	}
	screens := Monitors()
	index := -1
	if _, err := fmt.Sscanf(s.target, "%d", &index); err != nil {
		return nil
	}
	if index < 1 || index > len(screens) {
		// Просили экран, которого нет: монитор отключили. Пишем весь стол —
		// запись без нужного угла лучше, чем её отсутствие.
		s.log.Warn("экран не найден, пишу весь рабочий стол", "экран", s.target)
		return nil
	}
	one := screens[index-1]
	// Ширина и высота обязаны быть чётными: иначе h264 откажется кодировать.
	return []string{
		"-offset_x", fmt.Sprint(one.Left),
		"-offset_y", fmt.Sprint(one.Top),
		"-video_size", fmt.Sprintf("%dx%d", one.Width-one.Width%2, one.Height-one.Height%2),
	}
}

// Stop — дописать запись и отдать путь. Пусто — записи не получилось.
func (s *ScreenRecorder) Stop() string {
	if s.command == nil {
		return ""
	}
	// «q» — просьба закончить штатно, дописав хвост файла. Убить процесс
	// значит оставить mp4 без оглавления, и его не откроет ни один плеер.
	if s.stdin != nil {
		_, _ = s.stdin.Write([]byte("q"))
		_ = s.stdin.Close()
	}

	done := make(chan struct{})
	go func() {
		_ = s.command.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(screenStopWait):
		_ = s.command.Process.Kill()
		<-done
	}
	s.command = nil

	info, err := os.Stat(s.path)
	if err != nil || info.Size() < 1024 {
		// Пустой файл — не запись: отдавать его человеку незачем.
		_ = os.Remove(s.path)
		s.log.Warn("запись экрана не сохранилась")
		return ""
	}
	return s.path
}

// Monitor — экран машины.
type Monitor struct {
	Left, Top, Width, Height int32
}

// Monitors — экраны машины, первым основной.
//
// Нужны, чтобы писать один экран, а не всё разом: на двух мониторах «весь
// рабочий стол» — это полоса, где половина кадра занята посторонним.
func Monitors() []Monitor {
	user32 := syscall.NewLazyDLL("user32.dll")
	enum := user32.NewProc("EnumDisplayMonitors")
	aware := user32.NewProc("SetProcessDPIAware")
	// Без этого Windows отдаёт размеры в «логических» точках, и вырезанная
	// область не совпадает с настоящим экраном на мониторе с масштабом.
	_, _, _ = aware.Call()

	found := make([]Monitor, 0, 2)
	callback := syscall.NewCallback(func(_, _ uintptr, rect *struct{ Left, Top, Right, Bottom int32 }, _ uintptr) uintptr {
		found = append(found, Monitor{
			Left: rect.Left, Top: rect.Top,
			Width: rect.Right - rect.Left, Height: rect.Bottom - rect.Top,
		})
		return 1
	})
	_, _, _ = enum.Call(0, 0, callback, 0)
	_ = unsafe.Pointer(nil)
	return found
}
