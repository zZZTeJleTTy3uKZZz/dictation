// Обновление «Диктовки» в значке: пункт «Проверить обновление», строка статуса,
// пункт установки и заметка на самом значке.
//
// ПОЧЕМУ ЗНАЧОК НЕ СКАЧИВАЕТ САМ. Манифест и файл проверяет и ставит служба: она
// живёт дольше значка и знает, идёт ли диктовка. Значок только просит и
// показывает ответ. Решения и тексты — в internal/trayupdate, здесь обвязка.
package main

import (
	"context"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/localdict"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/trayupdate"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/updates"
)

// updatePoll — как часто значок спрашивает службу о её состоянии. Служба сама
// ходит на манифест раз в шесть часов, так что спрашивать чаще незачем: это
// просто чтение памяти службы. Открытия меню лотку не сообщают, поэтому
// свежесть держим опросом, а не нажатием.
const updatePoll = 3 * time.Minute

const (
	// updatePause — пауза между чтениями состояния, пока служба работает.
	updatePause = time.Second
	// checkLimit — сколько ждём итога проверки; служба сама режет сеть на 30 с.
	checkLimit = 40 * time.Second
	// downloadLimit — скачивание большого установщика по медленной сети.
	downloadLimit = 15 * time.Minute
)

// localUpdates — настоящая служба: её маршруты обновления по адресу из
// endpoint.json, как у окна.
type localUpdates struct{}

func (localUpdates) State(ctx context.Context) (updates.State, error) {
	return localdict.UpdateState(ctx)
}

func (localUpdates) Check(ctx context.Context) (updates.State, error) {
	return localdict.UpdateCheck(ctx)
}

func (localUpdates) Download(ctx context.Context) (updates.State, error) {
	return localdict.UpdateDownload(ctx)
}

func (localUpdates) Install(ctx context.Context) (updates.State, error) {
	return localdict.UpdateInstall(ctx)
}

// updateMenu — пункты обновления в меню и последняя подсказка значка.
type updateMenu struct {
	line    *systray.MenuItem
	install *systray.MenuItem
	check   *systray.MenuItem
	// tip — подсказка, которая сейчас стоит у значка: systray перерисовывать
	// незачем, если она не поменялась.
	tip string
}

// updateMemory — что значок помнит об обновлении между опросами.
type updateMemory struct {
	guard sync.Mutex
	state updates.State
	err   error
	// busy — идёт проверка или установка: второй клик не запускает вторую.
	busy bool
}

// trayTip — подсказка значка по умолчанию.
const trayTip = "MeetDigest — разбор встреч"

// remember запоминает ответ службы для меню и значка.
func (t *Tray) remember(state updates.State, err error) {
	t.update.guard.Lock()
	t.update.state, t.update.err = state, err
	t.update.guard.Unlock()
}

// updateView отдаёт последний ответ службы.
func (t *Tray) updateView() (updates.State, error) {
	t.update.guard.Lock()
	defer t.update.guard.Unlock()
	return t.update.state, t.update.err
}

// claimUpdate занимает проверку или установку; false — уже идёт другая.
func (t *Tray) claimUpdate() bool {
	t.update.guard.Lock()
	defer t.update.guard.Unlock()
	if t.update.busy {
		return false
	}
	t.update.busy = true
	return true
}

func (t *Tray) releaseUpdate() {
	t.update.guard.Lock()
	t.update.busy = false
	t.update.guard.Unlock()
}

// pollUpdate — редкий опрос состояния обновления. Сеть здесь не трогаем: служба
// читает манифест сама, значку нужно только её ответ.
func (t *Tray) pollUpdate(ctx context.Context) {
	for {
		state, err := t.upd.State(ctx)
		t.remember(state, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(updatePoll):
		}
	}
}

// checkUpdate — пункт «Проверить обновление».
func (t *Tray) checkUpdate(ctx context.Context) {
	if !t.claimUpdate() {
		return
	}
	defer t.releaseUpdate()
	t.remember(updates.State{Status: updates.StatusChecking}, nil)
	state, err := trayupdate.Check(ctx, t.upd, updatePause, checkLimit)
	t.remember(state, err)
	if err != nil {
		t.log.Warn("проверка обновления не вышла", "err", err)
		return
	}
	t.log.Info("проверка обновления", "статус", state.Status, "версия", state.Version)
}

// installUpdate — пункт «Обновить до …»: скачать и поставить через службу.
//
// Во время записи установку не запускаем: служба отложила бы её сама, но
// человек должен видеть причину в меню, а не молчание.
func (t *Tray) installUpdate(ctx context.Context) {
	if !t.claimUpdate() {
		return
	}
	defer t.releaseUpdate()
	state, _ := t.updateView()
	busy := t.dictation.Going() || t.store.Read().LiveRecording()
	result, err := trayupdate.Update(ctx, t.upd, state, busy, updatePause, downloadLimit)
	t.remember(result, err)
	if err != nil {
		t.log.Warn("обновление не запустилось", "err", err)
		return
	}
	t.log.Info("обновление", "статус", result.Status, "версия", result.Version)
}

// paintUpdate — пункты обновления по последнему ответу службы. Вызывается из
// refresh каждые две секунды: только читает память, сети не трогает.
func (t *Tray) paintUpdate(menu *updateMenu) {
	state, err := t.updateView()
	if line := trayupdate.Line(state, err); line != "" {
		menu.line.SetTitle(line)
		menu.line.Show()
	} else {
		menu.line.Hide()
	}

	if trayupdate.Offer(state) {
		menu.install.SetTitle(trayupdate.InstallTitle(state))
		menu.install.Show()
	} else {
		menu.install.Hide()
	}

	if tip := trayupdate.Tooltip(state); tip != menu.tip {
		menu.tip = tip
		if tip == "" {
			tip = trayTip
		}
		systray.SetTooltip(tip)
	}
}

// updateBadge — нужна ли значку заметка «есть обновление».
func (t *Tray) updateBadge() bool {
	state, _ := t.updateView()
	return trayupdate.Badge(state)
}

// iconWithUpdate — значок для состояния, с точкой обновления, если она нужна.
// Если точку нарисовать не вышло, значок остаётся обычным: заметка не повод
// потерять значок целиком.
func iconWithUpdate(badge string, dot bool) []byte {
	base := iconFor(badge)
	if !dot {
		return base
	}
	made, err := trayupdate.WithDot(base)
	if err != nil {
		return base
	}
	return made
}
