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
	// shown — что сейчас стоит в пунктах и у значка. Пункт трогаем, только если
	// вид поменялся: systray на каждый Hide у спрятанного пункта пишет в журнал
	// «unable to hideMenuItem», а меню перерисовывается раз в две секунды.
	shown trayupdate.MenuView
}

// updateMemory — что значок помнит об обновлении между опросами.
type updateMemory struct {
	guard sync.Mutex
	// mem — ответы службы и счёт неудач подряд: строку «служба не запущена»
	// показываем, только когда неудач больше одной.
	mem trayupdate.Memory
	// busy — идёт проверка или установка: второй клик не запускает вторую.
	busy bool
}

// trayTip — подсказка значка по умолчанию.
const trayTip = "MeetDigest — разбор встреч"

// remember запоминает ответ службы для меню и значка.
func (t *Tray) remember(state updates.State, err error) {
	t.update.guard.Lock()
	t.update.mem.Remember(state, err)
	t.update.guard.Unlock()
}

// showLocal ставит местный ответ, не учитывая его в счёте неудач.
func (t *Tray) showLocal(state updates.State) {
	t.update.guard.Lock()
	t.update.mem.Local(state)
	t.update.guard.Unlock()
}

// markChecking ставит промежуточное «Проверяю». Это не обращение к службе, и
// счёт неудач оно не трогает: иначе строка «служба не запущена» пропадала бы
// на время проверки.
func (t *Tray) markChecking() {
	t.update.guard.Lock()
	t.update.mem.Checking()
	t.update.guard.Unlock()
}

// updateView отдаёт последний ответ службы; причина недоступности — только
// если служба не отвечает несколько раз подряд.
func (t *Tray) updateView() (updates.State, error) {
	t.update.guard.Lock()
	defer t.update.guard.Unlock()
	return t.update.mem.Read()
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
	t.markChecking()
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
	result, reached, err := trayupdate.Update(ctx, t.upd, state, busy, updatePause, downloadLimit)
	switch {
	case err != nil || reached:
		// Ответ или ошибка службы — это итог обращения и идёт в счёт.
		t.remember(result, err)
	default:
		// Местный ответ (идёт запись): счёт недоступности не трогаем, иначе
		// строка «служба не запущена» гасла бы из-за того, чего служба не знала.
		t.showLocal(result)
	}
	if err != nil {
		t.log.Warn("обновление не запустилось", "err", err)
		return
	}
	t.log.Info("обновление", "статус", result.Status, "версия", result.Version)
}

// paintUpdate — пункты обновления по последнему ответу службы. Вызывается из
// refresh каждые две секунды: только читает память, сети не трогает. Пункт
// меняется только тогда, когда меняется его вид.
func (t *Tray) paintUpdate(menu *updateMenu) {
	state, err := t.updateView()
	view := trayupdate.View(state, err)
	prev := menu.shown
	menu.shown = view

	applyItem(menu.line, prev.Line, view.Line)
	applyItem(menu.install, prev.Install, view.Install)
	if view.Tip != prev.Tip {
		tip := view.Tip
		if tip == "" {
			tip = trayTip
		}
		systray.SetTooltip(tip)
	}
}

// applyItem приводит пункт к тексту next; пустой текст — пункт спрятан.
func applyItem(item *systray.MenuItem, prev, next string) {
	if next == prev {
		return
	}
	if next == "" {
		item.Hide()
		return
	}
	item.SetTitle(next)
	if prev == "" {
		item.Show()
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
