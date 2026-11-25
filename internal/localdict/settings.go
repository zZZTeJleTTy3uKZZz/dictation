// Настройки диктовки, которые служба отдаёт окну сама.
//
// ЗАЧЕМ ЭТО В СЛУЖБЕ. Окно диктовки ставится отдельным пакетом, и ядра
// настроек (meetdigest-core.exe) в этом пакете нет. Если способ распознавания,
// провайдер и язык живут только в ядре, окно остаётся без настроек — или
// заводит вторую их копию, которая расходится с первой. Поэтому источник один:
// служба, а окно спрашивает и пересылает.
//
// ЧТО ЛЕЖИТ ГДЕ. Способ, язык и модель — в voice.json, как у значка и заметок:
// выбирать способ должно быть можно в любом окне, и второй файл разошёлся бы с
// первым. Местных моделей в диктовке больше нет, поэтому models.json не нужен.
// Горячая клавиша — в config.json, потому что её регистрирует служба.
package localdict

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/providercfg"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
)

// recognitionCloud — прежнее имя просьбы «распознавай по сети». Старое окно
// ещё присылает её; местного пути больше нет, и просьба ничего не меняет.
const recognitionCloud = "cloud"

// removedLegacySpeech — ключ убранного запасного сервиса в старых voice.json.
// Миграция обязана его понимать: у уже настроенных людей файл не трогаем, а
// вписанный адрес переезжает в «свой провайдер». Имя собрано из частей, чтобы
// публичное дерево не несло название прежнего сервиса.
const removedLegacySpeech = "neur" + "aldeep"

// DefaultSpeechModel — модель сети по умолчанию: та, ради которой диктовка и
// заведена. Наших адресов и приставок к имени модели здесь больше нет:
// по умолчанию провайдер не задан вовсе, а этот ответ — только
// подпись для окна, пока человек ничего не выбрал.
const DefaultSpeechModel = speech.GroqModel

// SettingChoice — пункт выбора в настройках: ключ для нас, название и
// объяснение для человека. Форма та же, что у ядра настроек: окно не должно
// знать, из какого процесса пришёл ответ.
type SettingChoice struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

// SettingsView — настройки диктовки так, как их видит окно.
//
// Местной модели здесь больше нет (решение владельца): остались провайдер,
// сочетание клавиш, микрофон, вставка с копированием и язык.
type SettingsView struct {
	// Speech — выбранный провайдер распознавания, Speeches — из чего выбирать.
	Speech     string          `json:"speech"`
	Speeches   []SettingChoice `json:"speeches"`
	SpeechNote string          `json:"speech_note"`
	// Providers — оба провайдера со своим признаком ключа: окно настраивает
	// каждый отдельно, не трогая второй. Ключ здесь не появляется никогда.
	Providers []ProviderView `json:"providers"`
	// Configured — провайдер настроен и может разбирать запись. Onboarding —
	// его ещё нет, и окно показывает первый запуск: два пути, ключ и проверка.
	Configured bool `json:"configured"`
	Onboarding bool `json:"onboarding"`
	// Proxy — посредник для запросов распознавания. Пароля здесь нет и быть не
	// должно: только признак, что он сохранён (защищённым, как ключ Groq).
	ProxyURL         string `json:"proxy_url,omitempty"`
	ProxyUser        string `json:"proxy_user,omitempty"`
	ProxyPasswordSet bool   `json:"proxy_password_set"`
	ProxyNote        string `json:"proxy_note,omitempty"`
	// SpeechModel и SpeechURL — сказанное человеком; ключ наружу не отдаётся,
	// только признак, что он задан.
	SpeechModel  string `json:"speech_model"`
	SpeechURL    string `json:"speech_url,omitempty"`
	SpeechKeySet bool   `json:"speech_key_set"`
	// Language и SpeechLanguage — одно и то же. Окно читает speech_language,
	// как у ядра; language оставлен, чтобы связка была понятна и снаружи.
	Language       string `json:"language"`
	SpeechLanguage string `json:"speech_language"`
	Hotkey         string `json:"hotkey"`
	// AudioKeepDays — через сколько дней удалять звук готовых записей (0 — никогда).
	// Storage — сколько места занимают записи; считается лениво и кэшируется.
	AudioKeepDays int         `json:"audio_keep_days"`
	Storage       StorageView `json:"storage"`
	// Microphone — состояние доступа к микрофону: unchecked, granted, denied.
	// Это не настройка, а ответ системы; меняется кнопкой /microphone.
	Microphone string `json:"microphone"`
}

// ProviderView — провайдер распознавания так, как его видит окно в настройках:
// задан ли его ключ, какие адрес и модель записаны. Ключа здесь нет никогда.
type ProviderView struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	KeySet bool   `json:"key_set"`
	URL    string `json:"url"`
	Model  string `json:"model"`
	// OwnURL — адрес вписан человеком (свой провайдер), а не взят у Groq.
	OwnURL bool `json:"own_url"`
}

// ProviderViews — оба провайдера, каждый со своим признаком ключа.
//
// Адрес Groq берётся из той же константы, что и у проверки и разбора. Своему
// провайдеру ключ ищется по адресу из настроек, и только если адрес есть:
// KeyForURL с пустым адресом отдал бы ключ Groq, и «ключ задан» у своего
// провайдера без адреса было бы ложью.
func ProviderViews(voice *prefs.Voice) []ProviderView {
	own := ProviderView{Key: "api", Title: "Свой провайдер", OwnURL: true}
	if voice != nil {
		own.URL = strings.TrimSpace(voice.SpeechURL)
		own.Model = strings.TrimSpace(voice.SpeechModel)
	}
	if own.URL != "" {
		own.KeySet = keyForVoice(voice).Set()
	}
	return []ProviderView{
		{
			Key:    "groq",
			Title:  "Groq",
			KeySet: speech.GroqKey(speech.ProvidersPath()).Set(),
			URL:    speech.GroqBaseURL,
			Model:  firstNonEmpty(groqRegistered().Model, speech.GroqModel),
		},
		own,
	}
}

// SpeechChoices — провайдеры распознавания, которые видит человек.
//
// Их ровно два: Groq со своим ключом и свой провайдер — ссылкой-конфигурацией
// или вписанный вручную. Третьего пункта в выборе нет: прежний запасной сервис
// убран, а список из трёх строк заставлял человека выбирать между
// тем, что и так работает.
func SpeechChoices(voice *prefs.Voice) []SettingChoice {
	return []SettingChoice{
		{Key: "groq", Title: "Groq", Note: cloudKeyNote("быстрый сервис")},
		{Key: "api", Title: "Свой провайдер (ссылка или вручную)", Note: ownServiceNote(voice)},
	}
}

// cloudKeyNote — что сказать про ключ провайдера: задан он или нет.
//
// Ключ лежит защищённым в providers.json, и окну он не нужен: человеку важно
// знать, вписан ли он, — иначе рабочий ключ вписывали бы второй раз.
func cloudKeyNote(fallback string) string {
	if speech.GroqKey(speech.ProvidersPath()).Set() {
		return fallback + ", ключ задан"
	}
	return fallback + ", ключа нет"
}

// ownServiceNote — что сказать про свой сервис: адрес вписан или нет.
func ownServiceNote(voice *prefs.Voice) string {
	if voice != nil && strings.TrimSpace(voice.SpeechURL) != "" {
		return "свой адрес и ключ"
	}
	return "нужен адрес и ключ"
}

// speechLanguage — язык из настроек; пусто — русский.
func speechLanguage(voice *prefs.Voice) string {
	if voice != nil {
		if value := strings.TrimSpace(voice.SpeechLanguage); value != "" {
			return value
		}
	}
	return defaultSpeechLanguage
}

// speechModel — модель сети, которой разбирается сейчас.
//
// Сказанное человеком главнее всего. Не сказано — берём модель, записанную у
// облачного провайдера: на этой машине там может быть прямой Groq, и подменять
// её умолчанием задачи значило бы сломать работающий разбор. И только когда
// нет ни того, ни другого, показываем умолчание задачи.
func speechModel(voice *prefs.Voice) string {
	if voice != nil {
		if value := strings.TrimSpace(voice.SpeechModel); value != "" {
			return value
		}
	}
	if found := groqRegistered(); strings.TrimSpace(found.Model) != "" {
		return found.Model
	}
	return DefaultSpeechModel
}

// explicitSpeechModel — только сказанное человеком; пусто — пусть способ сам
// возьмёт модель провайдера. Нужно там, где подстановка умолчания перебила бы
// уже настроенный прямой Groq.
func explicitSpeechModel(voice *prefs.Voice) string {
	if voice == nil {
		return ""
	}
	return strings.TrimSpace(voice.SpeechModel)
}

// SpeechNote — чем разбирается сейчас, словами для человека. Окно показывает
// эту строку рядом с выбором провайдера.
func SpeechNote(voice *prefs.Voice) string {
	if !speechConfigured(voice) {
		// Ни выбора, ни ключа: это первый запуск, и человеку нужно не «проверьте
		// ключ», а «выберите провайдера».
		return "провайдер не настроен: выберите Groq или своего провайдера на первом запуске"
	}
	channel := cloudChannel(voice, nil, nil, nil)
	if channel == nil {
		if intended := intendedCloud(voice); intended != nil {
			return "«" + intended.Title() + "» пока не готов: проверьте ключ"
		}
		return "облачное распознавание не настроено: впишите ключ к сервису"
	}
	return "«" + channel.Title() + "» по сети"
}

// ReadSettings собирает настройки из файлов и состояния. Без замка: зовётся и
// из Snapshot (замок уже взят), и из HTTP-обработчика.
func ReadSettings(voice *prefs.Voice, hotkey, microphone string) SettingsView {
	if voice == nil {
		voice = prefs.LoadVoice("")
	}
	language := speechLanguage(voice)
	found := chosenSpeech(voice)
	proxy := speech.ReadProxy(speech.ProvidersPath())
	return SettingsView{
		Speech:           found,
		Speeches:         SpeechChoices(voice),
		SpeechNote:       SpeechNote(voice),
		Providers:        ProviderViews(voice),
		Configured:       speechConfigured(voice),
		Onboarding:       !speechConfigured(voice),
		SpeechModel:      speechModel(voice),
		SpeechURL:        strings.TrimSpace(voice.SpeechURL),
		SpeechKeySet:     speechKeySet(voice),
		ProxyURL:         proxy.URL,
		ProxyUser:        proxy.User,
		ProxyPasswordSet: proxy.PasswordSet,
		ProxyNote:        proxy.Why,
		Language:         language,
		SpeechLanguage:   language,
		Hotkey:           hotkey,
		Microphone:       microphone,
	}
}

// speechConfigured — провайдер настроен и может разбирать запись.
//
// Это не «человек что-то выбрал», а «есть чем ходить»: адрес и ключ у своего
// провайдера, ключ у Groq. Пока этого нет, окно показывает первый запуск, и
// диктовка честно говорит, что разбирать нечем, вместо того чтобы стучаться в
// наш прежний роутер.
func speechConfigured(voice *prefs.Voice) bool {
	switch chosenSpeech(voice) {
	case "api":
		return voice != nil && strings.TrimSpace(voice.SpeechURL) != "" && keyForVoice(voice).Set()
	case "groq":
		return speech.GroqKey(speech.ProvidersPath()).Set()
	default:
		return false
	}
}

// keyForVoice — ключ своего провайдера по адресу из настроек: ключ Groq не
// станет ключом чужого сервиса, а ключ чужого не уйдёт нашему роутеру.
func keyForVoice(voice *prefs.Voice) speech.ProviderKey {
	if voice == nil {
		return speech.ProviderKey{}
	}
	return speech.KeyForURL(speech.ProvidersPath(), voice.SpeechURL)
}

// chosenSpeech — какой провайдер выбран. Пустое поле и «auto» значат Groq: это
// умолчание диктовки, и отдельного пункта «ничего не выбрано» человеку не
// показывают.
func chosenSpeech(voice *prefs.Voice) string {
	name := ""
	if voice != nil {
		name = strings.ToLower(strings.TrimSpace(voice.Speech))
	}
	switch name {
	case "api":
		return "api"
	case "groq":
		return "groq"
	case removedLegacySpeech:
		// Прежний запасной сервис убран. Вписанный адрес при этом
		// не теряем: человек уже сказал, куда ходить, и это его провайдер. Без
		// адреса провайдер не настроен — окно ведёт на первый запуск.
		if voice != nil && strings.TrimSpace(voice.SpeechURL) != "" {
			return "api"
		}
		return ""
	}
	// Прежние «local», «gigaam» и «auto» означали местную модель. Если при них
	// вписан свой адрес, он и остаётся рабочим: человек уже сказал, куда ходить,
	// и терять это при переходе на облако нельзя.
	if voice != nil && strings.TrimSpace(voice.SpeechURL) != "" {
		return "api"
	}
	// Выбора нет, но ключ Groq уже вписан (так было до отказа от местной модели):
	// показываем Groq, а не пустой выбор.
	if speech.GroqKey(speech.ProvidersPath()).Set() {
		return "groq"
	}
	// Ничего не настроено: провайдера по умолчанию больше нет, и решает
	// человек на первом запуске.
	return ""
}

// speechKeySet — задан ли ключ выбранного провайдера. Сам ключ наружу не
// отдаётся: окну он не нужен, а в журналах и снимках экрана ему не место.
func speechKeySet(voice *prefs.Voice) bool {
	switch chosenSpeech(voice) {
	case "":
		return false
	case "api":
		return voice != nil && keyForVoice(voice).Set()
	default:
		return speech.GroqKey(speech.ProvidersPath()).Set()
	}
}

// Settings — настройки работающей службы.
func (a *App) Settings() SettingsView {
	a.mu.Lock()
	config, microphone := a.state.Config, a.state.Microphone
	a.mu.Unlock()
	if config.Hotkey == "" {
		config.Hotkey = prefs.DefaultHotkey
	}
	view := ReadSettings(a.voice(), config.Hotkey, microphone)
	view.AudioKeepDays = config.AudioKeepDays
	view.Storage = a.storageView()
	return view
}

// ChangeSettings применяет присланные поля и возвращает новое состояние.
//
// Принимаются и поля по имени, и просьба в форме ядра настроек
// ({"action":"speech","value":"…"}): окно уже умеет говорить вторым способом, и
// переучивать его ради нового адреса незачем.
func (a *App) ChangeSettings(fields map[string]any) error {
	if fields == nil {
		return errors.New("пустая просьба")
	}
	if action, ok := stringValue(fields["action"]); ok {
		value, _ := stringValue(fields["value"])
		fields = map[string]any{actionField(action): value}
	}
	if err := applyProxyFields(fields); err != nil {
		return err
	}
	voice := prefs.LoadVoice("")
	// Адрес и ключ из одной просьбы — ОДНА правка по своему значению, а не два
	// значения из словаря. Словарь идёт в случайном порядке, и «адрес от одной
	// просьбы, ключ от другой» — это ключ одного сервиса, ушедший другому.
	address, addressGiven := strings.TrimSpace(voice.SpeechURL), false
	if raw, given := fields["speech_url"]; given {
		if value, _ := stringValue(raw); strings.TrimSpace(value) != "" {
			address, addressGiven = strings.TrimSpace(value), true
		}
	}
	// СВЕРКА ДО ПРАВОК. Ключ из просьбы пишется строго по адресу (имя ключа — из
	// адреса, а не у вызывающего) и сверяется обратным чтением, и только потом
	// трогаются настройки. Не сошлось — просьба не выполнена целиком, настройки
	// прежние. Хранилище не прочиталось — тоже, но там ещё и прежняя копия ключа
	// не стирается: не прочитали — значит, не знаем, что там, и терять нечем.
	keyFromRequest := false
	if raw, given := fields["speech_key"]; given {
		if key, _ := stringValue(raw); strings.TrimSpace(key) != "" {
			if _, err := speech.SetKeyChecked(speech.ProvidersPath(), address, key); err != nil {
				return err
			}
			keyFromRequest = true
		}
	}
	// Адрес без ключа. Просьба выбора провайдера — свой разговор: сервис без
	// ключа там допустим (настройки без провайдера, так задумано на первом
	// запуске), и чужой адрес её ключом не наделяется. А когда ключ/адрес
	// трогают поодиночке через настройки, новый адрес обязан носить свой ключ:
	// включённый сервис без ключа — отказ на каждой диктовке.
	if _, toggling := fields["speech"]; addressGiven && !keyFromRequest && !toggling {
		probe := *voice
		probe.SpeechURL = address
		if !speechConfigured(&probe) {
			return errors.New("впишите ключ провайдера: сервис без ключа работать не будет")
		}
	}
	changed := false
	for name, raw := range fields {
		name = actionField(name)
		switch name {
		case "speech", "recognition":
			if name == "recognition" {
				// Окно прежней версии присылало «recognition»: у него были
				// только сеть и местная модель. Сеть — это выбор провайдера,
				// которого не назвали; местная модель больше не существует, и
				// просьба о ней молча означает сеть (решение владельца).
				// Местной модели больше нет. Просьбу «сделай местной» не
				// выполняем и уже настроенный способ не затираем: старый
				// человек ничего не потерял, а разбирать всё равно будет
				// облако.
				continue
			}
			value, _ := stringValue(raw)
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "groq":
				voice.Speech = "groq"
			case "api":
				voice.Speech = "api"
			case removedLegacySpeech:
				// Пункт убран из окна: просьбу старого окна
				// выполняем отказом с причиной, а не молчаливым выбором.
				return errors.New("прежнего запасного способа больше нет: выберите Groq или своего провайдера")
			default:
				return fmt.Errorf("нет такого способа распознавания: %s", value)
			}
			changed = true
		case "language", "speech_language":
			value, _ := stringValue(raw)
			voice.SpeechLanguage = strings.TrimSpace(value)
			changed = true
		case "speech_model":
			value, _ := stringValue(raw)
			voice.SpeechModel = strings.TrimSpace(value)
			changed = true
		case "speech_url":
			value, _ := stringValue(raw)
			voice.SpeechURL = strings.TrimSpace(value)
			changed = true
		case "speech_key":
			// Ключ своего провайдера лежит в общем хранилище ядра (на Windows —
			// под DPAPI), а не открытым текстом в voice.json, который
			// пересылают. Ключ привязан к адресу: сменился адрес — прежний ключ
			// новому сервису не достаётся.
			//
			// Ключ уже записан и сверен по адресу выше (сверка до правок):
			// отсюда только правка открытого поля voice.json. Не сошлось или
			// хранилище не прочиталось — сюда не дошли, и прежнее поле цело.
			// Стирается оно и тогда, когда ключ в просьбе пуст: пустое поле у
			// окна значит «убери открытую копию», а не «оставь».
			voice.SpeechKey = ""
			changed = true
		case "hotkey":
			value, _ := stringValue(raw)
			if strings.TrimSpace(value) == "" {
				return errors.New("пустое сочетание клавиш")
			}
			a.mu.Lock()
			config := a.state.Config
			a.mu.Unlock()
			config.Hotkey = value
			if err := a.Configure(config); err != nil {
				return err
			}
		case "audio_keep_days":
			// Срок хранения звука живёт в config.json рядом с сочетанием: его
			// читает уборка службы, а сохраняет Configure с проверкой. Пустое или
			// не число — отказ, а не молчаливый ноль («никогда»).
			value, _ := stringValue(raw)
			days, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return errors.New("срок хранения звука: нужен 0, 1, 7, 14, 30 или 90 дней")
			}
			a.mu.Lock()
			config := a.state.Config
			a.mu.Unlock()
			config.AudioKeepDays = days
			if err := a.Configure(config); err != nil {
				return err
			}
		case "local_model", "local_download":
			// Прежнее окно ещё умело выбирать и скачивать местную модель.
			// Местной модели больше нет, и просьба ничего не меняет: отказ
			// старому окну не нужен, а человеку — тем более.
		case "microphone":
			// Кнопка «разрешить микрофон» открывает настройки Windows: сама
			// программа разрешение выдать не может.
			if value, _ := stringValue(raw); value == "open" || value == "settings" || value == "true" {
				OpenMicrophoneSettings()
			}
		default:
			return fmt.Errorf("неизвестная настройка: %s", name)
		}
	}
	if changed {
		return voice.Save()
	}
	return nil
}

// applyProxyFields — записать прокси, если он пришёл в просьбе.
//
// Поля прокси применяются вместе и сразу: отдельные правки адреса и пароля
// оставляли бы на мгновение рабочий адрес с прежним паролем. Пароль
// записывается защищённым (см. speech.SaveProxy); пустое поле значит «не
// трогать», а не «стереть» — окно не присылает сохранённый пароль обратно.
func applyProxyFields(fields map[string]any) error {
	urlValue, hasURL := stringValue(fields["proxy_url"])
	userValue, hasUser := stringValue(fields["proxy_user"])
	passwordValue, hasPassword := stringValue(fields["proxy_password"])
	if !hasURL && !hasUser && !hasPassword {
		return nil
	}
	delete(fields, "proxy_url")
	delete(fields, "proxy_user")
	delete(fields, "proxy_password")

	current := speech.ReadProxy(speech.ProvidersPath())
	if hasURL {
		current.URL = strings.TrimSpace(urlValue)
	}
	if hasUser {
		current.User = strings.TrimSpace(userValue)
	}
	if err := providercfg.ValidateProxy(providercfg.Proxy{
		URL: current.URL, User: current.User,
	}); err != nil {
		return err
	}
	var password *string
	if hasPassword {
		password = &passwordValue
	}
	return speech.SaveProxy("", current, password)
}

// actionField — имя поля для просьбы в форме ядра.
func actionField(action string) string {
	switch strings.TrimSpace(action) {
	case "speech-language":
		return "language"
	case "speech-model":
		return "speech_model"
	case "speech-url":
		return "speech_url"
	case "local-model":
		return "local_model"
	case "local-download":
		return "local_download"
	default:
		return strings.TrimSpace(action)
	}
}

// stringValue — значение из JSON как строка: окно присылает строки, а
// самопроверки могут прислать число (язык, порог) — приводим, не падая.
func stringValue(raw any) (string, bool) {
	switch value := raw.(type) {
	case string:
		return value, true
	case fmt.Stringer:
		return value.String(), true
	case nil:
		return "", false
	default:
		return fmt.Sprint(value), true
	}
}

// settingsRoutes — GET/POST/PUT /settings: окно читает и меняет настройки
// диктовки, не зная про meetdigest-core.exe.
//
// POST принимается наравне с PUT: окно диктовки ходит в службу своим простым
// клиентом, который выбирает метод по наличию тела (GET без тела, POST с телом).
// Городить ради метода второй клиент незачем.
func (a *App) settingsRoutes(mux *http.ServeMux) {
	read := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.Settings())
	}
	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) { read(w) })
	change := func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		var fields map[string]any
		if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
			http.Error(w, "просьба не разобрана: нужен JSON с полями настроек", http.StatusBadRequest)
			return
		}
		if err := a.ChangeSettings(fields); err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "recording") {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		read(w)
	}
	mux.HandleFunc("POST /settings", change)
	mux.HandleFunc("PUT /settings", change)
}
