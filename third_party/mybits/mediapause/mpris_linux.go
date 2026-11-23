//go:build linux

package mediapause

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/godbus/dbus/v5"
)

const (
	mprisPath   = "/org/mpris/MediaPlayer2"
	mprisPlayer = "org.mpris.MediaPlayer2.Player"
)

// Supported — есть ли на этой системе чем управлять плеерами.
func Supported() bool { return true }

// NewDefault подключается к сессионной шине. Нет шины (сервер, контейнер) —
// ошибка; вызывающий просто не паузит.
func NewDefault() (Backend, error) {
	// Без адреса шины библиотека пробует dbus-launch и может поднять чужой
	// демон; на сервере или в контейнере лучше честно ничего не делать.
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		if _, err := os.Stat(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "bus")); err != nil {
			return nil, errNoBus
		}
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNoBus, err)
	}
	return newMPRIS(&dbusBus{conn: conn}), nil
}

type dbusBus struct{ conn *dbus.Conn }

func (d *dbusBus) Names(ctx context.Context) ([]string, error) {
	var names []string
	if err := d.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
		return nil, err
	}
	return names, nil
}

func (d *dbusBus) Status(ctx context.Context, name string) (string, error) {
	var value dbus.Variant
	call := d.conn.Object(name, mprisPath).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, mprisPlayer, "PlaybackStatus")
	if err := call.Store(&value); err != nil {
		return "", err
	}
	status, _ := value.Value().(string)
	return status, nil
}

func (d *dbusBus) Call(ctx context.Context, name, method string) error {
	return d.conn.Object(name, mprisPath).CallWithContext(ctx, mprisPlayer+"."+method, 0).Err
}

func (d *dbusBus) Close() error { return d.conn.Close() }
