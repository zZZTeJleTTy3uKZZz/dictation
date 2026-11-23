// Диктовка из значка: горячая клавиша, плашка и текст в поле.
//
// ПОЧЕМУ ЯДРО И ПЛАШКА ЖИВУТ ПОСТОЯННО. Раньше нажатие клавиши запускало две
// программы: ядро (оно поднимало модель — четыре секунды) и плашку (оконный
// движок — ещё пара секунд). В сумме человек нажимал и ждал пять секунд, прежде
// чем увидеть хоть что-то, — а нажимал он ровно затем, чтобы сэкономить время.
//
// Теперь обе поднимаются вместе со значком и ждут. Нажатие — это одно слово в
// ядро, и плашка появляется мгновенно: она уже нарисована, ей осталось только
// показаться.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/appdirs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hidden"
)

// Dictation — постоянно живущая связка «ядро + плашка».
type Dictation struct {
	mu      sync.Mutex
	core    *exec.Cmd
	overlay *exec.Cmd
	tell    io.WriteCloser
	going   bool
	ready   bool
}

// Going — идёт ли диктовка.
func (d *Dictation) Going() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.going
}

// Ready — поднялась ли связка.
func (d *Dictation) Ready() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ready
}

// Rise — поднять ядро и плашку. Зовётся один раз при запуске значка.
func (d *Dictation) Rise(log logger) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.core != nil {
		return nil
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("не найти себя на диске: %w", err)
	}
	beside := filepath.Dir(self)

	core := hidden.Command(filepath.Join(beside, "meetdigest-core.exe"), "dictate")
	// Жалобы ядра — в файл рядом с журналом: у значка нет консоли, и без этого
	// «ядро умерло» выглядит как «ничего не произошло».
	if trouble, err := os.OpenFile(filepath.Join(homeFolder(), "dictation.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		core.Stderr = trouble
	}
	tell, err := core.StdinPipe()
	if err != nil {
		return fmt.Errorf("ядро не слушает: %w", err)
	}
	events, err := core.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ядро молчит: %w", err)
	}
	if err := core.Start(); err != nil {
		return fmt.Errorf("ядро не запустилось: %w", err)
	}

	// Плашка читает события ядра прямо со своего ввода: значок посередине не
	// нужен, и лишнее звено между речью и картинкой добавило бы задержку.
	overlay := hidden.Command(filepath.Join(beside, "meetdigest-ui.exe"), "--overlay")
	overlay.Stdin = events
	// А обратно — через нас: кнопки «Стоп» и «Отмена» плашка пишет в свой вывод,
	// мы передаём их ядру.
	answers, err := overlay.StdoutPipe()
	if err != nil {
		return fmt.Errorf("плашка не отвечает: %w", err)
	}
	if err := overlay.Start(); err != nil {
		// Плашка не поднялась — диктовать всё равно можно: текст важнее картинки.
		log.Warn("плашка не открылась, диктую без неё", "err", err)
		go func() { _, _ = io.Copy(io.Discard, events) }()
		overlay = nil
	} else {
		go d.listen(answers, log)
	}

	d.core, d.overlay, d.tell, d.ready = core, overlay, tell, true
	log.Info("диктовка готова к работе")

	// Если ядро или плашка умрут — поднимем их заново на следующем нажатии.
	go func() {
		_ = core.Wait()
		d.mu.Lock()
		d.core, d.overlay, d.tell, d.ready, d.going = nil, nil, nil, false, false
		d.mu.Unlock()
		if overlay != nil {
			_ = overlay.Process.Kill()
			_ = overlay.Wait()
		}
		log.Warn("ядро диктовки закончилось")
	}()
	return nil
}

// listen — слушать ответы плашки («стоп», «отмена»).
func (d *Dictation) listen(answers io.Reader, log logger) {
	reader := bufio.NewScanner(answers)
	for reader.Scan() {
		word := strings.TrimSpace(reader.Text())
		if word != "stop" && word != "cancel" {
			continue
		}
		d.say(word)
		d.mu.Lock()
		d.going = false
		d.mu.Unlock()
	}
}

// toggleDictation — нажали клавишу или пункт меню.
func (t *Tray) toggleDictation() {
	if !t.dictation.Ready() {
		// Первое нажатие после падения ядра: поднимаем и сразу начинаем.
		if err := t.dictation.Rise(t.log); err != nil {
			t.log.Error("диктовка не поднялась", "err", err)
			return
		}
		// Ядру нужно мгновение, чтобы открыть микрофон.
		time.Sleep(300 * time.Millisecond)
	}
	if t.dictation.Going() {
		t.dictation.Finish()
		t.log.Info("диктовка закончилась")
		return
	}
	t.dictation.Begin()
	t.log.Info("диктовка началась")
}

// Begin — начать диктовку: одно слово ядру, которое уже готово.
func (d *Dictation) Begin() {
	d.say("start")
	d.mu.Lock()
	d.going = true
	d.mu.Unlock()
}

// Finish — закончить диктовку.
//
// Просим словом, а не убиваем: последний кусок речи обычно самый важный, и он
// должен успеть распознаться и напечататься.
func (d *Dictation) Finish() {
	d.say("stop")
	d.mu.Lock()
	d.going = false
	d.mu.Unlock()
}

// Quit — погасить связку при выходе.
func (d *Dictation) Quit() {
	d.say("quit")
	d.mu.Lock()
	overlay := d.overlay
	d.mu.Unlock()
	if overlay != nil && overlay.Process != nil {
		_ = overlay.Process.Kill()
	}
}

func (d *Dictation) say(word string) {
	d.mu.Lock()
	tell := d.tell
	d.mu.Unlock()
	if tell == nil {
		return
	}
	_, _ = fmt.Fprintln(tell, word)
}

// homeFolder — папка настроек; своя функция, чтобы не тащить сюда весь config.
func homeFolder() string {
	home := appdirs.HomeDir()
	if home == "" {
		return "."
	}
	return filepath.Join(home, ".meetdigest")
}

// logger — то немногое, что нужно от журнала.
type logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	Info(msg string, args ...any)
}
