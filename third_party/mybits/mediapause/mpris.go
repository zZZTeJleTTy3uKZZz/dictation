package mediapause

import (
	"context"
	"errors"
	"strings"
)

// MPRIS — стандарт плееров Linux на сессионной шине D-Bus: каждый плеер
// занимает имя org.mpris.MediaPlayer2.* и отвечает на Pause/Play. Логика не
// зависит от библиотеки D-Bus, поэтому живёт здесь и проверяется на любой ОС.

const mprisPrefix = "org.mpris.MediaPlayer2."

// mprisBus — то, что нужно от шины.
type mprisBus interface {
	Names(ctx context.Context) ([]string, error)
	Status(ctx context.Context, name string) (string, error)
	Call(ctx context.Context, name, method string) error
	Close() error
}

type mprisBackend struct{ bus mprisBus }

func newMPRIS(bus mprisBus) *mprisBackend { return &mprisBackend{bus: bus} }

func (b *mprisBackend) Playing(ctx context.Context) ([]Target, error) {
	names, err := b.bus.Names(ctx)
	if err != nil {
		return nil, err
	}
	var out []Target
	for _, name := range names {
		if !strings.HasPrefix(name, mprisPrefix) {
			continue
		}
		status, err := b.bus.Status(ctx, name)
		if err != nil || status != "Playing" {
			continue
		}
		out = append(out, Target{ID: name, Name: strings.TrimPrefix(name, mprisPrefix), handle: name})
	}
	return out, nil
}

// busName — имя плеера на шине: его кладёт Playing в handle.
func busName(t Target) string {
	if name, ok := t.handle.(string); ok {
		return name
	}
	return t.ID
}

func (b *mprisBackend) Pause(ctx context.Context, t Target) error {
	return b.bus.Call(ctx, busName(t), "Pause")
}

func (b *mprisBackend) Paused(ctx context.Context, t Target) (bool, error) {
	status, err := b.bus.Status(ctx, busName(t))
	if err != nil {
		return false, err
	}
	return status == "Paused", nil
}

func (b *mprisBackend) Play(ctx context.Context, t Target) error {
	return b.bus.Call(ctx, busName(t), "Play")
}

func (b *mprisBackend) Release(Target) {}

func (b *mprisBackend) Close() {
	if b.bus != nil {
		_ = b.bus.Close()
	}
}

var errNoBus = errors.New("mediapause: нет сессионной шины D-Bus")
