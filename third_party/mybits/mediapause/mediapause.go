// Package mediapause ставит на паузу всё, что сейчас играет в системе
// (Windows: SMTC, Linux: MPRIS по D-Bus), а потом возобновляет только то, что
// остановил сам. Нужен программам, которые слушают микрофон (диктовка, запись
// встречи): музыка и видео в браузере не должны попадать в запись.
//
// Правила, на которых всё держится:
//
//   - Pause и Resume не ждут системы: работа идёт в фоне, поэтому запись
//     начинается в тот же миг, а пауза наступает параллельно.
//   - Возобновляется только то, что поставили на паузу МЫ и что до сих пор на
//     паузе. Если человек за это время сам запустил или остановил плеер,
//     пакет не трогает его.
//   - Любая ошибка системы проглатывается (сообщается в Log): нет плеера, нет
//     шины, отказ приложения — это «ничего не делаем», а не сбой вызывающего.
//   - Вне Windows и Linux пакет ничего не делает (Supported() == false).
package mediapause

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrUnsupported — на этой системе управлять плеерами нечем.
var ErrUnsupported = errors.New("mediapause: система не поддерживается")

// Target — один играющий плеер. ID и Name — для журнала; handle принадлежит
// бэкенду и снаружи не читается.
type Target struct {
	ID, Name string
	handle   any
}

// Backend — то, что умеет система. Все методы зовутся по одному за раз.
type Backend interface {
	// Playing — плееры, которые играют прямо сейчас.
	Playing(ctx context.Context) ([]Target, error)
	// Pause ставит плеер на паузу; ошибка — плеер отказал или пропал.
	Pause(ctx context.Context, t Target) error
	// Paused — плеер до сих пор на паузе (а не играет и не закрыт).
	Paused(ctx context.Context, t Target) (bool, error)
	// Play возобновляет плеер.
	Play(ctx context.Context, t Target) error
	// Release отпускает ресурсы плеера; зовётся ровно один раз на каждый Target.
	Release(t Target)
	// Close отпускает бэкенд целиком.
	Close()
}

// Options — настройки Pauser.
type Options struct {
	// Timeout — сколько времени отведено одной фоновой работе (пауза или
	// возобновление). По умолчанию 4 секунды.
	Timeout time.Duration
	// Log получает короткие события: pause, resume, skip, error. Только
	// счётчики и имена приложений, никаких заголовков и текстов.
	Log func(event, detail string)
}

// Pauser — пауза и возобновление за одну сессию записи.
type Pauser struct {
	backend Backend
	timeout time.Duration
	log     func(event, detail string)

	mu   sync.Mutex
	tail chan struct{} // закрывается, когда кончится последняя поставленная работа
	held []Target      // что поставили на паузу мы
	wg   sync.WaitGroup
}

// New собирает Pauser над бэкендом. Бэкенд nil — Pauser, который ничего не делает.
func New(backend Backend, options Options) *Pauser {
	p := &Pauser{backend: backend, timeout: options.Timeout, log: options.Log}
	if p.timeout <= 0 {
		p.timeout = 4 * time.Second
	}
	if p.log == nil {
		p.log = func(string, string) {}
	}
	return p
}

// Pause в фоне ставит на паузу всё играющее. Возвращается сразу. Повторный
// вызов, пока пауза не снята, ничего не добавляет: уже остановленное нами
// остаётся нашим, а то, что человек запустил сам после первой паузы, не трогаем.
func (p *Pauser) Pause() {
	p.enqueue(p.doPause)
}

// Resume в фоне возобновляет то, что поставил на паузу Pause. Возвращается
// сразу; если Pause ещё работает, Resume дождётся его в фоне.
func (p *Pauser) Resume() {
	p.enqueue(p.doResume)
}

// Warm заранее поднимает системное управление (первое обращение дороже
// остальных), ничего не останавливая. Вызывается один раз при старте программы.
func (p *Pauser) Warm() {
	p.enqueue(func(ctx context.Context) {
		targets, err := p.backend.Playing(ctx)
		if err != nil {
			p.log("warm", "нет управления плеерами: "+err.Error())
		}
		for _, target := range targets {
			p.backend.Release(target)
		}
	})
}

// Wait ждёт всю поставленную в фон работу. Нужен при выходе и в проверках.
func (p *Pauser) Wait() {
	if p != nil {
		p.wg.Wait()
	}
}

// Close дожидается работы и отпускает бэкенд.
func (p *Pauser) Close() {
	if p == nil {
		return
	}
	p.Wait()
	if p.backend != nil {
		p.backend.Close()
	}
}

func (p *Pauser) enqueue(work func(ctx context.Context)) {
	if p == nil || p.backend == nil {
		return
	}
	p.mu.Lock()
	previous := p.tail
	done := make(chan struct{})
	p.tail = done
	p.wg.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.wg.Done()
		defer close(done)
		if previous != nil {
			<-previous
		}
		ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
		defer cancel()
		defer func() {
			// Бэкенд не должен ронять программу, которая только слушает микрофон.
			if recovered := recover(); recovered != nil {
				p.log("error", "panic в бэкенде")
			}
		}()
		work(ctx)
	}()
}

func (p *Pauser) doPause(ctx context.Context) {
	if len(p.held) > 0 {
		p.log("skip", "уже на паузе")
		return
	}
	started := time.Now()
	playing, err := p.backend.Playing(ctx)
	if err != nil {
		p.log("error", "список плееров: "+err.Error())
		return
	}
	for _, target := range playing {
		if err := p.backend.Pause(ctx, target); err != nil {
			p.log("error", "пауза "+target.Name+": "+err.Error())
			p.backend.Release(target)
			continue
		}
		p.held = append(p.held, target)
		p.log("pause", "app="+target.Name)
	}
	p.log("paused", "count="+itoa(len(p.held))+" of="+itoa(len(playing))+" ms="+itoa(int(time.Since(started).Milliseconds())))
}

func (p *Pauser) doResume(ctx context.Context) {
	held := p.held
	p.held = nil
	for _, target := range held {
		still, err := p.backend.Paused(ctx, target)
		switch {
		case err != nil:
			p.log("skip", "app="+target.Name+" плеер недоступен")
		case !still:
			// Человек успел сам запустить, остановить или закрыть плеер.
			p.log("skip", "app="+target.Name+" состояние изменилось")
		default:
			if err := p.backend.Play(ctx, target); err != nil {
				p.log("error", "возобновление "+target.Name+": "+err.Error())
			} else {
				p.log("resume", "app="+target.Name)
			}
		}
		p.backend.Release(target)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	negative := n < 0
	if negative {
		n = -n
	}
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		digits[i] = '-'
	}
	return string(digits[i:])
}
