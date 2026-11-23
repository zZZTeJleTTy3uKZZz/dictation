// Диктофон в значке: заметка голосом по сочетанию клавиш.
//
// ПОЧЕМУ ИМЕННО ЗДЕСЬ. Значок живёт в сеансе человека всё время, пока он за
// компьютером, — ровно когда и нужна заметка. Воркер для этого не годится: он
// работает в задаче планировщика, у которой нет доступа к клавиатуре сеанса.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fyne.io/systray"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/config"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/dictaphone"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hotkey"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/notes"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/recorder"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
)

// startVoice — поднять диктофон, если он включён в настройках.
//
// Возвращает его же: значку надо показывать в меню, что идёт запись заметки.
func (t *Tray) startVoice(ctx context.Context, device string) *dictaphone.Dictaphone {
	settings := prefs.LoadVoice("")
	if !settings.On {
		t.log.Info("диктофон выключен в настройках")
		return nil
	}

	combo, err := hotkey.Parse(settings.Hotkey)
	if err != nil {
		// Непонятное сочетание не оставляет человека без диктофона: берём
		// значение по умолчанию и говорим, что именно не так.
		t.log.Warn("сочетание не разобрано, беру обычное",
			"сочетание", settings.Hotkey, "err", err)
		combo, err = hotkey.Parse(hotkey.DefaultCombo)
		if err != nil {
			t.log.Error("даже обычное сочетание не разобралось", "err", err)
			return nil
		}
	}

	made := dictaphone.New(
		notes.NewStore(notes.DefaultPath(config.Home())),
		voiceSpeech(settings),
		device,
		t.log,
	)
	made.Open = func() dictaphone.Tape {
		return recorder.NewVoice(notes.AudioFolder(config.Home()), t.log)
	}
	// Способ расшифровки читается на каждую заметку: человек меняет его в
	// настройках, не перезапуская значок.
	made.Channel = func() string { return prefs.LoadVoice("").Speech }

	press := func() {
		// Нажатие обрабатывается в потоке сообщений Windows — держать его нельзя,
		// иначе следующее нажатие придёт с задержкой.
		//
		// Клавиша ведёт диктовку, а не заметку: человек нажимает её, когда хочет
		// сказать вместо того, чтобы печатать, и текст обязан оказаться в поле.
		// Заметка без поля осталась пунктом меню.
		//
		// Про нажатие пишем в журнал: без этой строки потом не отличить «человек
		// не нажимал» от «нажал, а значок не услышал».
		t.log.Info("нажали сочетание", "сочетание", combo.String())
		go t.toggleDictation()
	}
	if err := hotkey.Listen(ctx, combo, t.log, press); err != nil {
		// Молчать нельзя. Человек жмёт клавишу, диктовка не поднимается, и он
		// не понимает, почему: чаще всего сочетание заняла другая программа,
		// а Windows в этом случае просто отказывает без единого слова.
		//
		// Поэтому говорим трижды: в журнал значка, в журнал диктовки (его
		// читает окно настроек диктофона) и прямо в лотке.
		t.hotkeyTrouble = hotkeyTrouble(combo)
		t.log.Error("диктофон без клавиши", "сочетание", combo.String(), "err", err)
		writeDictationLog(t.hotkeyTrouble)
		systray.SetTooltip("MeetDigest — " + t.hotkeyTrouble)
		return made // меню всё равно покажет «записать заметку»
	}
	return made
}

// hotkeyTrouble — что сказать человеку, когда сочетание занять не удалось.
//
// Без имени клавиши и без подсказки, где её менять, это сообщение бесполезно:
// человек не знает ни что сломалось, ни куда идти.
func hotkeyTrouble(combo hotkey.Combo) string {
	return fmt.Sprintf("Сочетание %s занято другой программой: диктовка по клавише не работает. "+
		"Выберите другое сочетание в настройках диктофона.", combo.String())
}

// writeDictationLog — строка в журнал диктовки.
//
// Отдельно от журнала значка: этот файл читает окно настроек диктофона, и по
// нему видно, почему клавиша молчит.
func writeDictationLog(line string) {
	log, err := os.OpenFile(filepath.Join(homeFolder(), "dictation.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer log.Close()
	fmt.Fprintf(log, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), line)
}

// voiceSpeech — способы расшифровки по настройкам человека.
//
// Служба диктовки идёт первой из готовых: она поднимается вместе с машиной, а
// внешняя программа грузит свою модель на каждый вызов. Местных моделей в
// диктовке больше нет (решение владельца): служба разбирает звук облаком, и
// остальные способы — тоже облачные.
func voiceSpeech(settings *prefs.Voice) *speech.Set {
	return speech.NewSet(
		speech.NewDictationService(),
		speech.NewOpenAICompatible(speech.RemoteFromVoice(settings)),
	)
}

// voiceLine — что показать в меню про заметку.
func voiceLine(made *dictaphone.Dictaphone) (string, bool) {
	if made == nil {
		return "", false
	}
	recording, passed := made.Recording()
	if !recording {
		return "Записать заметку (" + currentHotkey() + ")", true
	}
	return formatVoice(passed), true
}

func formatVoice(passed time.Duration) string {
	whole := int(passed.Seconds() + 0.5)
	if whole < 60 {
		return fmt.Sprintf("Закончить заметку · %d с", whole)
	}
	return fmt.Sprintf("Закончить заметку · %d:%02d", whole/60, whole%60)
}

// currentHotkey — какое сочетание сейчас настроено (для подписи в меню).
func currentHotkey() string {
	settings := prefs.LoadVoice("")
	if combo, err := hotkey.Parse(settings.Hotkey); err == nil {
		return combo.String()
	}
	return hotkey.DefaultCombo
}
