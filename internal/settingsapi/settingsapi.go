// Package settingsapi — настройки одним входом для любого интерфейса.
//
// ЗАЧЕМ. Интерфейсов у клиента может быть сколько угодно: окно на WebView2,
// окно на Flutter, значок в лотке, когда-нибудь телефон. А правила — одни:
// какие режимы бывают, что значит «всегда» для браузера, какую папку нельзя
// добавить дважды. Повтори эти правила в Dart — и они разойдутся: в одном месте
// поправили, в другом забыли, и человек увидит разное в двух окнах.
//
// Поэтому правила живут здесь, на Go, а интерфейс только показывает и просит.
// Снаружи это выглядит как две операции: Read (покажи всё) и Apply (сделай).
package settingsapi

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hotkey"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/localdict"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/recorder"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/sources"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
)

// Choice — пункт выбора: ключ для нас, название для человека.
type Choice struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

// Folder — папка под наблюдением.
type Folder struct {
	Title string `json:"title"`
	Path  string `json:"path"`
	// Provider — папку дал провайдер (Телемост), а не человек: убрать её можно
	// только выключив провайдера, иначе она вернётся при следующем обходе.
	Provider bool `json:"provider"`
}

// App — решение, принятое по программе.
type App struct {
	Program  string `json:"program"`
	Title    string `json:"title"`
	Decision string `json:"decision"`
}

// View — всё, что показывает окно настроек.
type View struct {
	Telemost     bool     `json:"telemost"`
	TelemostNote string   `json:"telemost_note"`
	Folders      []Folder `json:"folders"`

	Window bool `json:"window"`
	Tray   bool `json:"tray"`

	Quality     string   `json:"quality"`
	QualityNote string   `json:"quality_note"`
	Qualities   []Choice `json:"qualities"`
	Screen      bool     `json:"screen"`
	// ScreenQualityNote — что значит выбранная подробность: человек должен
	// видеть цену решения (вес часа записи), не открывая список.
	ScreenQuality     string   `json:"screen_quality"`
	ScreenQualityNote string   `json:"screen_quality_note"`
	ScreenQualities   []Choice `json:"screen_qualities"`
	ScreenTarget      string   `json:"screen_target"`
	Screens           []Choice `json:"screens"`
	ScreensNote       string   `json:"screens_note"`
	RecordFolder      string   `json:"record_folder"`

	Mode     string   `json:"mode"`
	ModeNote string   `json:"mode_note"`
	Modes    []Choice `json:"modes"`

	Decided   []App    `json:"decided"`
	Decisions []Choice `json:"decisions"`

	// ── диктофон ──
	Voice       bool     `json:"voice"`
	Hotkey      string   `json:"hotkey"`
	Hotkeys     []Choice `json:"hotkeys"`
	HotkeyNote  string   `json:"hotkey_note"`
	Speech      string   `json:"speech"`
	Speeches    []Choice `json:"speeches"`
	SpeechNote  string   `json:"speech_note"`
	SpeechURL   string   `json:"speech_url"`
	SpeechModel string   `json:"speech_model"`
	// SpeechLanguage — на каком языке речь: пусто — русский.
	SpeechLanguage string `json:"speech_language"`
	// SpeechKeySet — ключ задан или нет. Сам ключ наружу не отдаём: окну он не
	// нужен, а в журналах и снимках экрана ему тем более не место.
	SpeechKeySet bool `json:"speech_key_set"`
	// GroqKeySet и GroqKeyNote — то же про ключ Groq, только лежит он не здесь,
	// а защищённым в настройках диктовки (providers.json). Окну нужно знать не
	// ключ, а задан ли он: иначе человек вписывал бы рабочий ключ второй раз.
	GroqKeySet  bool   `json:"groq_key_set"`
	GroqKeyNote string `json:"groq_key_note"`
	// GroqURL и GroqModel — что записано про Groq: адрес и модель. Показываются,
	// а не правятся полем: и то и другое лежит в providers.json рядом с ключом.
	GroqURL   string `json:"groq_url"`
	GroqModel string `json:"groq_model"`
}

// Read — собрать состояние настроек.
//
// Читается заново каждый раз: настройки меняет ещё и плашка встречи, и
// показывать запомненное значило бы врать о том, что действует сейчас.
func Read() View {
	list := sources.Load("")
	display := prefs.LoadDisplay("")
	recording := prefs.LoadRecording("")
	settings := prefs.Load("")
	voice := prefs.LoadVoice("")

	return View{
		Telemost:     list.Telemost,
		TelemostNote: telemostNote(list),
		Folders:      folders(list),

		Window: display.Window,
		Tray:   display.Tray,

		Quality:           recording.Quality,
		QualityNote:       noteOf(prefs.Quality, recording.Quality),
		Qualities:         choices(prefs.Quality, prefs.QualityOrder),
		Screen:            recording.Screen,
		ScreenQuality:     recording.ScreenQuality,
		ScreenQualityNote: noteOf(prefs.ScreenQuality, recording.ScreenQuality),
		ScreenQualities:   choices(prefs.ScreenQuality, prefs.ScreenQualityOrder),
		ScreenTarget:      recording.ScreenTarget,
		Screens:           screenChoices(),
		ScreensNote:       screensNote(),
		RecordFolder:      recording.Directory(),

		Mode:     settings.Mode,
		ModeNote: modeNote(settings.Mode),
		Modes:    modeChoices(),

		Decided:   decided(settings),
		Decisions: decisionChoices(),

		Voice:        voice.On,
		Hotkey:       voice.Hotkey,
		Hotkeys:      hotkeyChoices(voice.Hotkey),
		HotkeyNote:   hotkeyNote(voice.Hotkey),
		Speech:       voice.Speech,
		Speeches:     speechChoices(voice),
		SpeechNote:   speechNote(voice),
		SpeechURL:    voice.SpeechURL,
		SpeechModel:  voice.SpeechModel,
		SpeechKeySet: speech.KeyForURL(speech.ProvidersPath(), voice.SpeechURL).Set(),

		SpeechLanguage: voice.SpeechLanguage,
		GroqKeySet:     speech.GroqKey(speech.ProvidersPath()).Set(),
		GroqKeyNote:    groqKeyNote(),
		GroqURL:        groqAddress(),
		GroqModel:      groqModel(),
	}
}

// groqAddress и groqModel — что записано про Groq: адрес и модель.
//
// Показываются человеку, а не правятся полем: и то и другое лежит в providers.json
// рядом с ключом, и правка там же — одна запись вместо двух.
func groqAddress() string {
	if found := groqRegistered(); found.BaseURL != "" {
		return found.BaseURL
	}
	return speech.GroqBaseURL
}

func groqModel() string {
	if found := groqRegistered(); found.Model != "" {
		return found.Model
	}
	// То же умолчание, что у службы: модель Groq, ради которой диктовка и
	// заведена. Показывать здесь другое имя значило бы обещать не ту модель,
	// которой идёт разбор.
	return localdict.DefaultSpeechModel
}

// groqRegistered — что записано про Groq в providers.json: адрес и модель.
func groqRegistered() speech.Registered {
	found, _ := speech.RegisteredProvider(speech.ProvidersPath(), speech.GroqProviderID, nil)
	return found
}

// groqKeyNote — словами для человека: задан ключ или нет и почему.
func groqKeyNote() string {
	found := speech.GroqKey(speech.ProvidersPath())
	if found.Set() {
		return "ключ задан, защищён Windows"
	}
	return found.Why
}

// speechSet — способы расшифровки, настроенные так, как просил человек.
//
// Порядок — порядок предпочтения, и он же порядок подмены: сперва облако (Groq
// отвечает за секунду и слышит имена верно), за ним местные способы. Запасного
// облака с другим адресом и ключом больше нет: второй сервис
// отвечал медленнее и стоил второго ключа. Заметка уезжает наружу только у
// облачных, и человек, которому этого не нужно, выбирает местный способ руками —
// тогда облако не трогают вовсе (см. speech.Set.Choose).
//
// speechSet — чем диктовка расшифровывает по настройкам человека.
//
// Местные модели из диктовки выведены (speech.ForDictation): работать ими можно
// и дальше — на странице моделей и в сравнении движков, — но в диктовке они не
// участвуют, и причина видна человеку в списке способов.
func speechSet(voice *prefs.Voice) *speech.Set {
	return speech.NewSet(
		speech.NewGroq("", voice.SpeechLanguage),
		// Общий сетевой сервис — последним: его адрес и ключ человек вписывает
		// сам, и он же остаётся тем способом, который был у него раньше.
		speech.NewOpenAICompatible(speech.RemoteFromVoice(voice)),
	)
}

// hotkeyChoices — сочетания на выбор плюс то, что человек уже выбрал.
//
// Своё сочетание остаётся в списке, даже если его нет среди подсказок: иначе
// окно показывало бы пустой выбор вместо работающей настройки.
func hotkeyChoices(chosen string) []Choice {
	found := make([]Choice, 0, len(hotkey.Suggestions())+1)
	known := false
	for _, one := range hotkey.Suggestions() {
		if one == chosen {
			known = true
		}
		found = append(found, Choice{Key: one, Title: one})
	}
	if !known && chosen != "" {
		found = append(found, Choice{Key: chosen, Title: chosen + " (своё)"})
	}
	return found
}

func hotkeyNote(chosen string) string {
	combo, err := hotkey.Parse(chosen)
	if err != nil {
		return "сочетание непонятно: " + err.Error()
	}
	return "нажать — начать заметку, нажать снова — закончить (" + combo.String() + ")"
}

func speechChoices(voice *prefs.Voice) []Choice {
	found := make([]Choice, 0, 4)
	for _, one := range speechSet(voice).Offers() {
		found = append(found, Choice{Key: one.Name, Title: one.Title, Note: one.Note})
	}
	return found
}

// speechNote — чем на самом деле будет расшифровано.
//
// Человек выбрал одно, а рабочим может оказаться другое (нет сети, нет модели) —
// и он должен это видеть заранее, а не обнаружить по заметке без текста.
func speechNote(voice *prefs.Voice) string {
	channel, why := speechSet(voice).Choose(voice.Speech)
	if channel == nil {
		return why
	}
	if why != "" {
		return why
	}
	return "расшифровывает «" + channel.Title() + "»"
}

// Apply — выполнить одну просьбу интерфейса.
//
// Имя действия и значение, а не набор полей: окно не присылает «вот всё
// состояние целиком», потому что между его открытием и нажатием настройки мог
// поменять кто-то ещё — и целиком присланное состояние молча откатило бы это.
func Apply(action, value string) error {
	switch action {
	case "telemost":
		list := sources.Load("")
		list.ToggleTelemost()
		return list.Save()
	case "add-folder":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("нужна папка")
		}
		list := sources.Load("")
		list.Add(value)
		return list.Save()
	case "remove-folder":
		list := sources.Load("")
		list.Remove(value)
		return list.Save()

	case "window", "tray":
		display := prefs.LoadDisplay("")
		if action == "window" {
			display.Window = !display.Window
		} else {
			display.Tray = !display.Tray
		}
		return display.Save()

	case "quality":
		if _, ok := prefs.Quality[value]; !ok {
			return fmt.Errorf("нет такого качества: %s", value)
		}
		return changeRecording(func(r *prefs.Recording) { r.Quality = value })
	case "screen":
		return changeRecording(func(r *prefs.Recording) { r.Screen = !r.Screen })
	case "screen-quality":
		if _, ok := prefs.ScreenQuality[value]; !ok {
			return fmt.Errorf("нет такой подробности экрана: %s", value)
		}
		return changeRecording(func(r *prefs.Recording) { r.ScreenQuality = value })
	case "screen-target":
		if !knownTarget(value) {
			return fmt.Errorf("нет такого экрана: %s", value)
		}
		return changeRecording(func(r *prefs.Recording) { r.ScreenTarget = value })
	case "record-folder":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("нужна папка")
		}
		return changeRecording(func(r *prefs.Recording) { r.Folder = value })

	case "mode":
		settings := prefs.Load("")
		before := settings.Mode
		settings.SetMode(value)
		if settings.Mode != value {
			// Неизвестный режим молча включил бы «спрашивать»: человек выбрал
			// одно, а получил другое и узнал бы об этом на следующей встрече.
			return fmt.Errorf("нет такого режима: %s (осталось %s)", value, before)
		}
		return settings.Save()

	case "quiet":
		settings := prefs.Load("")
		if settings.QuietUntil > 0 {
			settings.Speak()
		} else {
			settings.BeQuiet(now())
		}
		return settings.Save()

	case "voice":
		return changeVoice(func(v *prefs.Voice) { v.On = !v.On })
	case "hotkey":
		// Сочетание проверяем здесь: непонятное или занятое нельзя записать в
		// настройки — человек нажимал бы и недоумевал, почему ничего нет.
		if _, err := hotkey.Parse(value); err != nil {
			return err
		}
		return changeVoice(func(v *prefs.Voice) { v.Hotkey = value })
	case "recognition":
		// Прежнее окно ещё присылает «сеть или местная модель». Местного пути
		// больше нет, и просьба ничего не меняет: уже настроенный способ не
		// затираем, а разбирать всё равно будет облако.
		return nil
	case "speech":
		if !knownSpeech(value) {
			return fmt.Errorf("нет такого способа расшифровки: %s", value)
		}
		// Местной модели в диктовке больше нет (решение владельца). Уже
		// сохранённый выбор остаётся известным, но выбрать его заново нельзя:
		// говорим причину, а не делаем вид, что выбрали.
		if strings.EqualFold(strings.TrimSpace(value), "gigaam") || strings.EqualFold(strings.TrimSpace(value), "local") {
			return fmt.Errorf("местной модели в диктовке больше нет: звук разбирает облако")
		}
		return changeVoice(func(v *prefs.Voice) { v.Speech = value })
	case "speech-url":
		return changeVoice(func(v *prefs.Voice) { v.SpeechURL = strings.TrimSpace(value) })
	case "speech-key":
		// Ключ своего провайдера лежит ТОЛЬКО в общем хранилище ядра (на
		// Windows — под DPAPI), а не открытым текстом в voice.json, который
		// пересылают. Ключ привязан к адресу: сменился адрес — прежний ключ
		// новому сервису не достаётся.
		//
		// Порядок строгий и тот же, что у /settings службы диктовки:
		// записать ключ по адресу (имя — из адреса, а не у вызывающего) →
		// прочитать обратно и сверить → и только потом стереть прежнее открытое
		// поле. Не сошлось и хранилище не прочиталось (ErrKeyUnreadable) —
		// прежнее остаётся, и voice.json не трогается вовсе: стереть его при
		// неудавшейся записи значило бы потерять ключ.
		voice := prefs.LoadVoice("")
		// Адрес, под которым ключ ляжет. Пустой speech_url — адрес Groq по
		// умолчанию: выбранный Groq (или пустой выбор) без адреса ходит именно
		// туда. Без этой подстановки ключ не записывался никуда (у пустого
		// адреса не было имени), а открытая копия стиралась.
		address := strings.TrimSpace(voice.SpeechURL)
		if address == "" {
			address = speech.GroqBaseURL
		}
		key := strings.TrimSpace(value)
		if key != "" {
			// Запись со сверкой: не сошлось или хранилище не прочиталось —
			// ошибка, и voice.json не трогается (ниже, не дошли).
			if _, err := speech.SetKeyChecked(speech.ProvidersPath(), address, key); err != nil {
				return err
			}
		}
		// Открытое поле не трогаем, если стирать нечего: правка не повод
		// переписывать файл.
		if strings.TrimSpace(voice.SpeechKey) == "" {
			return nil
		}
		return changeVoice(func(v *prefs.Voice) { v.SpeechKey = "" })
	case "speech-model":
		return changeVoice(func(v *prefs.Voice) { v.SpeechModel = strings.TrimSpace(value) })
	case "speech-language":
		return changeVoice(func(v *prefs.Voice) { v.SpeechLanguage = strings.TrimSpace(value) })
	case "groq-key":
		// Ключ Groq лежит не в наших настройках, а защищённым в настройках
		// диктовки — рядом с ключами причёсывания, тем же способом. Пишем его
		// оттуда же, откуда читаем: два места для одного ключа разошлись бы.
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("нужен ключ Groq")
		}
		return speech.RegisterGroqKey(speech.ProvidersPath(), value)

	case "glossary-add":
		// Словарь правится и человеком через окно, и мной через файлы — обоими
		// путями через этот случай, чтобы правила не разошлись.
		_, err := speech.AddGlossaryTerm(speech.ProvidersPath(), value)
		return err
	case "glossary-remove":
		_, err := speech.RemoveGlossaryTerm(speech.ProvidersPath(), value)
		return err

	case "note-text":
		// Режется по первому «=»: в тексте заметки их сколько угодно, а
		// номер заметки их не содержит вовсе.
		id, text, found := strings.Cut(value, "=")
		if !found {
			return fmt.Errorf("нужно «номер=текст»")
		}
		return NoteText(id, text)
	case "app":
		program, decision, ok := strings.Cut(value, "=")
		if !ok {
			return fmt.Errorf("нужно «программа=решение»")
		}
		settings := prefs.Load("")
		switch decision {
		case "always":
			settings.AllowAlways(program)
		case "never":
			settings.Forbid(program)
		case "ask":
			settings.Forget(program)
		default:
			return fmt.Errorf("нет такого решения: %s", decision)
		}
		return settings.Save()
	}
	return fmt.Errorf("непонятное действие: %s", action)
}

// now — вынесено отдельной функцией: проверки двигают время руками.
var now = time.Now

func changeVoice(change func(*prefs.Voice)) error {
	voice := prefs.LoadVoice("")
	change(voice)
	return voice.Save()
}

// knownSpeech — такой способ расшифровки есть?
func knownSpeech(value string) bool {
	if value == "auto" {
		return true
	}
	for _, one := range speechSet(prefs.LoadVoice("")).Offers() {
		if one.Name == value {
			return true
		}
	}
	return false
}

func changeRecording(change func(*prefs.Recording)) error {
	recording := prefs.LoadRecording("")
	change(recording)
	return recording.Save()
}

// knownTarget — такой экран есть?
func knownTarget(value string) bool {
	if value == "all" {
		return true
	}
	for _, one := range screenChoices() {
		if one.Key == value {
			return true
		}
	}
	return false
}

// ── подписи и списки ──────────────────────────────────────────────────────

func folders(list *sources.Sources) []Folder {
	found := make([]Folder, 0, len(list.Folders)+1)
	if list.Telemost {
		for _, folder := range sources.TelemostDirectories() {
			found = append(found, Folder{
				Title: filepath.Base(folder), Path: folder, Provider: true,
			})
		}
	}
	for _, folder := range list.Folders {
		found = append(found, Folder{Title: filepath.Base(folder), Path: folder})
	}
	return found
}

func telemostNote(list *sources.Sources) string {
	if !list.Telemost {
		return "провайдер выключен — записи Телемоста не отправляются"
	}
	found := sources.TelemostDirectories()
	if len(found) == 0 {
		return "папка появится при первой записи"
	}
	return strings.Join(found, "; ")
}

func screensNote() string {
	monitors := recorder.Monitors()
	if len(monitors) < 2 {
		return "экран один — писать можно только его"
	}
	return fmt.Sprintf("экранов: %d · «все» даст полосу из обоих", len(monitors))
}

func screenChoices() []Choice {
	found := []Choice{{Key: "all", Title: "все экраны"}}
	for index, monitor := range recorder.Monitors() {
		found = append(found, Choice{
			Key:   fmt.Sprint(index + 1),
			Title: fmt.Sprintf("экран %d", index+1),
			Note:  fmt.Sprintf("%d×%d", monitor.Width, monitor.Height),
		})
	}
	return found
}

// choices — пункты в заданном порядке.
//
// Порядок задан списком, а не обходом карты: обход карты в Go случаен, и выбор
// перетряхивался бы при каждом открытии окна.
func choices(table map[string][3]string, order []string) []Choice {
	found := make([]Choice, 0, len(order))
	for _, key := range order {
		if value, ok := table[key]; ok {
			found = append(found, Choice{Key: key, Title: value[0], Note: value[1]})
		}
	}
	return found
}

// noteOf — пояснение к выбранному значению.
//
// Оно показывается под названием настройки: человек должен видеть, что значит
// его выбор, не открывая список.
func noteOf(table map[string][3]string, key string) string {
	if value, ok := table[key]; ok {
		return value[1]
	}
	return ""
}

// modeNote — пояснение к режиму.
func modeNote(key string) string {
	_, note := prefs.Mode(key)
	return note
}

func modeChoices() []Choice {
	found := make([]Choice, 0, len(prefs.Modes))
	for _, mode := range prefs.Modes {
		found = append(found, Choice{Key: mode.Key, Title: mode.Title, Note: mode.Note})
	}
	return found
}

func decisionChoices() []Choice {
	return []Choice{
		{Key: "always", Title: "записывать без вопроса"},
		{Key: "never", Title: "никогда не записывать"},
		{Key: "ask", Title: "спрашивать"},
	}
}

func decided(settings *prefs.Prefs) []App {
	found := make([]App, 0, len(settings.Always)+len(settings.Never))
	for _, program := range settings.Always {
		found = append(found, App{Program: program, Title: AppTitle(program), Decision: "always"})
	}
	for _, program := range settings.Never {
		found = append(found, App{Program: program, Title: AppTitle(program), Decision: "never"})
	}
	return sameNamesApart(found)
}

// sameNamesApart — развести решения, которые выглядят одинаково.
//
// У Teams две программы (ms-teams.exe и teams.exe), у браузеров тоже бывает по
// две. В списке они становились двумя строками «Teams» подряд — и выглядело это
// как удвоение, а не как два разных решения. Добавляем имя файла — но только там,
// где название и правда повторяется: у одиночных строк оно только мешает.
func sameNamesApart(all []App) []App {
	counts := map[string]int{}
	for _, one := range all {
		counts[one.Title]++
	}
	for index, one := range all {
		if counts[one.Title] > 1 {
			all[index].Title = one.Title + " · " + one.Program
		}
	}
	return all
}

// AppTitle — как программа называется по-человечески.
func AppTitle(program string) string {
	if known, ok := knownApps[program]; ok {
		return known
	}
	return program
}

var knownApps = map[string]string{
	"yandextelemost.exe": "Телемост",
	"zoom.exe":           "Zoom",
	"ms-teams.exe":       "Teams",
	"teams.exe":          "Teams",
	"telegram.exe":       "Телеграм",
	"discord.exe":        "Discord",
	"skype.exe":          "Skype",
	"chrome.exe":         "Chrome",
	"msedge.exe":         "Edge",
	"browser.exe":        "Яндекс Браузер",
	"firefox.exe":        "Firefox",
}
