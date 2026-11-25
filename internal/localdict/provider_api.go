// Первый запуск: проверка провайдера, его сохранение и ссылка-конфигурация.
//
// ЗАЧЕМ ЭТИ ЗАПРОСЫ, А НЕ ОДНИ НАСТРОЙКИ. У окна на первом запуске ещё нет
// ничего: человек вписывает ключ, нажимает «Проверить» и только потом соглашается
// сохранить. Если бы проверка шла через сохранение, неверный ключ уже лежал бы в
// настройках, а «проверить» перестало бы значить «проверить».
//
// КЛЮЧ НАРУЖУ НЕ ОТДАЁТСЯ НИКОГДА. Окно присылает его сюда и получает только
// признаки: принят ли, задан ли. Ссылку-конфигурацию собирает тоже служба — ключ
// Groq лежит защищённым, и прочитать его может только она.
package localdict

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/providercfg"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
)

// providerRequest — что окно прислало про провайдера.
//
// Поля прокси — указатели нарочно: «не прислали» и «прислали пустым» значат
// разное. В первом случае проверка идёт через уже сохранённый прокси, во втором
// — напрямую. Строкой это не различить.
type providerRequest struct {
	Speech string `json:"speech"`
	URL    string `json:"url"`
	Model  string `json:"model"`
	Key    string `json:"key"`

	ProxyURL      *string `json:"proxy_url"`
	ProxyUser     *string `json:"proxy_user"`
	ProxyPassword *string `json:"proxy_password"`
}

// providerRoutes — проверка, сохранение, ссылка и вставка ссылки.
func (a *App) providerRoutes(mux *http.ServeMux) {
	read := func(w http.ResponseWriter, r *http.Request, into any) bool {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		if err := json.NewDecoder(r.Body).Decode(into); err != nil {
			http.Error(w, "просьба не разобрана: нужен JSON", http.StatusBadRequest)
			return false
		}
		return true
	}
	write := func(w http.ResponseWriter, answer any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	}

	mux.HandleFunc("POST /provider/check", func(w http.ResponseWriter, r *http.Request) {
		var ask providerRequest
		if !read(w, r, &ask) {
			return
		}
		result, err := a.checkProvider(r.Context(), ask)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		write(w, result)
	})

	mux.HandleFunc("POST /provider/apply", func(w http.ResponseWriter, r *http.Request) {
		var ask providerRequest
		if !read(w, r, &ask) {
			return
		}
		if err := a.applyProvider(ask); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		write(w, a.Settings())
	})

	// Стереть ключ выбранного способа: кнопка «Удалить» в настройках. Ключ
	// наружу не отдаётся ни до, ни после — ответ тот же, что у apply.
	mux.HandleFunc("POST /provider/forget", func(w http.ResponseWriter, r *http.Request) {
		var ask struct {
			Speech string `json:"speech"`
		}
		if !read(w, r, &ask) {
			return
		}
		if err := a.forgetProvider(ask.Speech); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		write(w, a.Settings())
	})

	mux.HandleFunc("POST /provider/share", func(w http.ResponseWriter, r *http.Request) {
		var ask struct {
			WithKey bool `json:"with_key"`
		}
		if !read(w, r, &ask) {
			return
		}
		link, err := a.providerLink(ask.WithKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		write(w, map[string]any{"link": link})
	})

	// Подсказка формата ссылки: её показывает поле ввода, а формат выпускает
	// ядро (providercfg). Одна подсказка на окно и на ядро — иначе окно
	// застрянет на прежней схеме (так было со старой dictation://).
	mux.HandleFunc("GET /provider/link/format", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]string{"format": providercfg.LinkFormat()})
	})

	mux.HandleFunc("POST /provider/link", func(w http.ResponseWriter, r *http.Request) {
		var ask struct {
			Link string `json:"link"`
		}
		if !read(w, r, &ask) {
			return
		}
		if err := a.applyProviderLink(ask.Link); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		write(w, a.Settings())
	})
}

// checkProvider — настоящая проверка ключа и адреса.
func (a *App) checkProvider(ctx context.Context, ask providerRequest) (providercfg.CheckResult, error) {
	target, err := a.checkTarget(ask)
	if err != nil {
		return providercfg.CheckResult{}, err
	}
	proxy, err := a.checkProxy(ask)
	if err != nil {
		return providercfg.CheckResult{}, err
	}
	return providercfg.Check(ctx, providercfg.CheckRequest{
		URL:      target.url,
		Key:      target.key,
		Model:    target.model,
		Language: target.language,
		Proxy:    proxy.Exit(),
	}), nil
}

// checkTarget — куда и с чем идти на проверку.
type checkTarget struct {
	url, key, model, language string
}

func (a *App) checkTarget(ask providerRequest) (checkTarget, error) {
	voice := a.voice()
	language := strings.TrimSpace(voice.SpeechLanguage)
	if language == "" {
		language = defaultSpeechLanguage
	}
	name := strings.ToLower(strings.TrimSpace(ask.Speech))
	model := strings.TrimSpace(ask.Model)
	key := strings.TrimSpace(ask.Key)
	switch name {
	case "", "api":
		address := strings.TrimSpace(ask.URL)
		if address == "" {
			address = strings.TrimSpace(voice.SpeechURL)
		}
		if address == "" {
			return checkTarget{}, errors.New("не указан адрес API вашего провайдера")
		}
		if _, err := providercfg.NormalizeBaseURL(address); err != nil {
			return checkTarget{}, err
		}
		if model == "" {
			model = strings.TrimSpace(voice.SpeechModel)
		}
		if model == "" {
			return checkTarget{}, errors.New("не указана модель распознавания")
		}
		if key == "" {
			// Ключ — по НОВОМУ адресу, а не из прежнего: человек проверяет
			// сервис, куда собирается перейти, и ключ прежнего сервиса ему там
			// не годится. Открытое поле voice.SpeechKey здесь не читается
			// вовсе — оно лишь источник переноса в хранилище (speech/secrets.go).
			found := speech.KeyForURL(speech.ProvidersPath(), address)
			if !found.Set() && !speech.KeyReadable(speech.ProvidersPath(), address) {
				// «Ключа нет» и «ключ не читается» — разное: во втором случае
				// он, скорее всего, лежит в хранилище, и совет «впишите ключ»
				// был бы советом в никуда.
				return checkTarget{}, errors.New(found.Why)
			}
			key = found.Key
		}
		return checkTarget{url: address, key: key, model: model, language: language}, nil
	case "groq":
		if key == "" {
			found := speech.GroqKey(speech.ProvidersPath())
			key = found.Key
		}
		if key == "" {
			return checkTarget{}, errors.New("впишите ключ Groq (он начинается с gsk" + "_)")
		}
		if model == "" {
			model = firstNonEmpty(groqRegistered().Model, speech.GroqModel)
		}
		return checkTarget{url: speech.GroqBaseURL, key: key, model: model, language: language}, nil
	default:
		return checkTarget{}, fmt.Errorf("нет такого провайдера: %s", ask.Speech)
	}
}

// checkProxy — через что проверять.
//
// Присланные поля главнее сохранённых: человек проверяет прокси до того, как
// согласился его сохранить. Пустой пароль при том же адресе и имени — это не
// «без пароля», а «оставь сохранённый»: окно не показывает пароль обратно.
func (a *App) checkProxy(ask providerRequest) (providercfg.Proxy, error) {
	if ask.ProxyURL == nil && ask.ProxyUser == nil && ask.ProxyPassword == nil {
		return storedProxy(), nil
	}
	found := providercfg.Proxy{}
	if ask.ProxyURL != nil {
		found.URL = strings.TrimSpace(*ask.ProxyURL)
	}
	if ask.ProxyUser != nil {
		found.User = strings.TrimSpace(*ask.ProxyUser)
	}
	if ask.ProxyPassword != nil {
		found.Password = *ask.ProxyPassword
	}
	if found.Password == "" {
		if saved := storedProxy(); sameProxyAddress(found, saved) {
			found.Password = saved.Password
		}
	}
	if err := providercfg.ValidateProxy(found); err != nil {
		return providercfg.Proxy{}, err
	}
	return found, nil
}

// storedProxy — сохранённый посредник.
func storedProxy() providercfg.Proxy {
	saved := speech.ReadProxy(speech.ProvidersPath())
	return providercfg.Proxy{URL: saved.URL, User: saved.User, Password: saved.Password}
}

// sameProxyAddress — тот же посредник (адрес и имя), а пароль мог не прийти.
func sameProxyAddress(one, other providercfg.Proxy) bool {
	return strings.EqualFold(strings.TrimSpace(one.URL), strings.TrimSpace(other.URL)) &&
		one.User == other.User
}

// applyProvider — сохранить провайдера.
func (a *App) applyProvider(ask providerRequest) error {
	name := strings.ToLower(strings.TrimSpace(ask.Speech))
	fields := map[string]any{}
	if proxy, present := requestProxyFields(ask); present {
		if err := providercfg.ValidateProxy(proxy); err != nil {
			return err
		}
		fields["proxy_url"] = proxy.URL
		fields["proxy_user"] = proxy.User
		if ask.ProxyPassword != nil {
			fields["proxy_password"] = *ask.ProxyPassword
		}
	}

	// Прежний ключ из providers.json и voice.json переезжает в хранилище
	// ДО того, как его собираются прочитать. Без этого «впишите ключ», хотя
	// человек его уже вписал прежним способом, — а исправный код при первом
	// же взгляде на хранилище считает его самим же (ensureMigrated), и
	// поведение под файлом у человека и без него стало бы разным.
	speech.Migrate(speech.ProvidersPath())

	switch name {
	case "groq":
		key := strings.TrimSpace(ask.Key)
		if key == "" {
			key = speech.GroqKey(speech.ProvidersPath()).Key
		}
		if key == "" {
			return errors.New("впишите ключ Groq (он начинается с gsk" + "_)")
		}
		if err := speech.RegisterGroqKey(speech.ProvidersPath(), key); err != nil {
			return err
		}
		// Ключ Groq прописывается и в providers.json (запись провайдера), хотя
		// читается из хранилища: без записи провайдера облако «Groq» считается
		// не заведённым, и выбор способа не на что опереть. Ключ в файл не
		// пишется: его место — хранилище.
		if err := speech.SaveGroqProvider(speech.ProvidersPath()); err != nil {
			return err
		}
		fields["speech"] = "groq"
	case "api":
		address, err := providercfg.NormalizeBaseURL(ask.URL)
		if err != nil {
			return err
		}
		model := strings.TrimSpace(ask.Model)
		if model == "" {
			return errors.New("не указана модель распознавания")
		}
		// Ключ приходит строго со своим адресом: имя ключа строится из нового
		// адреса, а не берётся у прежнего. Ключ пишется в хранилище и читается
		// обратно ДО записи настроек: не сошлось — сервис без ключа не окажется
		// включённым, а прежнее открытое поле не стёрто.
		key := strings.TrimSpace(ask.Key)
		if key == "" {
			// Ключ у нового адреса — свой, а не прежнего сервиса. Хранилище не
			// прочиталось — это не «ключа нет»: адрес не переключаем и говорим
			// причину, а прежние настройки не трогаем вовсе.
			found := speech.KeyForURL(speech.ProvidersPath(), address)
			if !speech.KeyReadable(speech.ProvidersPath(), address) {
				return errors.New(found.Why)
			}
			if !found.Set() {
				// Причина — та же, что у пробной проверки (found.Why): у
				// своего провайдера это «у записи нет ключа», и ничего другого
				// здесь сказать нельзя.
				return errors.New(found.Why)
			}
		}
		if key != "" {
			// Запись со сверкой: не сошлось или хранилище не прочиталось —
			// настройки не меняются, и прежняя копия ключа остаётся.
			if _, err := speech.SetKeyChecked(speech.ProvidersPath(), address, key); err != nil {
				return err
			}
		}
		fields["speech"] = "api"
		fields["speech_url"] = address
		fields["speech_model"] = model
		// Прежнее открытое место ключа стирается после успешной записи и
		// сверки нового — иначе она осталась бы третьей копией секрета.
		fields["speech_key"] = ""
	default:
		return errors.New("выберите провайдера: Groq или свой сервис")
	}
	return a.ChangeSettings(fields)
}

// requestProxyFields — прокси из просьбы, если его вообще прислали.
func requestProxyFields(ask providerRequest) (providercfg.Proxy, bool) {
	if ask.ProxyURL == nil && ask.ProxyUser == nil && ask.ProxyPassword == nil {
		return providercfg.Proxy{}, false
	}
	found := providercfg.Proxy{}
	if ask.ProxyURL != nil {
		found.URL = strings.TrimSpace(*ask.ProxyURL)
	}
	if ask.ProxyUser != nil {
		found.User = strings.TrimSpace(*ask.ProxyUser)
	}
	if ask.ProxyPassword != nil {
		found.Password = *ask.ProxyPassword
	}
	return found, true
}

// providerLink — ссылка-конфигурация текущего провайдера.
func (a *App) providerLink(withKey bool) (string, error) {
	voice := a.voice()
	switch chosenSpeech(voice) {
	case "groq":
		registered := groqRegistered()
		key := speech.GroqKey(speech.ProvidersPath()).Key
		if key == "" {
			return "", errors.New("ключ Groq ещё не вписан — делиться нечем")
		}
		link := providercfg.Link{
			Name:  "Groq",
			URL:   firstNonEmpty(registered.BaseURL, speech.GroqBaseURL),
			Model: firstNonEmpty(registered.Model, speech.GroqModel),
		}
		if withKey {
			link.Key = key
		}
		return providercfg.Build(link)
	case "api":
		if strings.TrimSpace(voice.SpeechURL) == "" {
			return "", errors.New("адрес своего провайдера не вписан — делиться нечем")
		}
		link := providercfg.Link{
			Name:  providercfg.HostOf(voice.SpeechURL),
			URL:   voice.SpeechURL,
			Model: voice.SpeechModel,
		}
		if withKey {
			// Ключ — тот, что принадлежит этому адресу: ключ нашего роутера не
			// уйдёт в ссылку чужого провайдера, даже если делиться с ключом
			// попросили.
			link.Key = speech.KeyForURL(speech.ProvidersPath(), voice.SpeechURL).Key
		}
		return providercfg.Build(link)
	default:
		return "", errors.New("провайдер ещё не настроен: сначала выберите его")
	}
}

// forgetProvider — стереть ключ способа и его адрес (для своего провайдера).
//
// ЗАЧЕМ ТАК. Стереть ключ — не значит молча сменить способ. Выбранный способ
// остаётся выбранным, а служба честно говорит «ключа нет»: разбор не пойдёт,
// пока ключ не вписан заново. Поэтому способ, который был подразумеваемым
// (пустое поле при наличии ключа или адреса), закрепляется явно: иначе после
// стирания выбор перескочил бы на другой провайдер.
//
// Порядок: сначала ключ в хранилище (со сверкой), и только потом настройки.
// Не удалось стереть — настройки прежние.
func (a *App) forgetProvider(name string) error {
	voice := a.voice()
	chosen := chosenSpeech(voice)
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "groq":
		if err := speech.ForgetGroqKey(speech.ProvidersPath()); err != nil {
			return err
		}
		if err := speech.ForgetGroqProvider(speech.ProvidersPath()); err != nil {
			return err
		}
		if chosen == "groq" {
			return a.ChangeSettings(map[string]any{"speech": "groq"})
		}
		return nil
	case "api":
		// Адреса нет — и ключа у своего провайдера нет. Стирать с пустым адресом
		// нельзя: имя ключа у пустого адреса — Groq, и ключ Groq ушёл бы вместе с
		// чужим. Поэтому пустой адрес просто пропускаем.
		// Адрес Groq, вписанный как «свой провайдер», тоже не стираем: его ключ —
		// это ключ Groq (одно имя в хранилище). Такой адрес очищаем из настроек,
		// а ключ Groq оставляем: пользоваться им может способ Groq.
		if address := strings.TrimSpace(voice.SpeechURL); address != "" && !isGroqHost(hostOf(address)) {
			if err := speech.ForgetKeyChecked(speech.ProvidersPath(), address); err != nil {
				return err
			}
		}
		fields := map[string]any{
			"speech_url":   "",
			"speech_model": "",
			"speech_key":   "",
		}
		if chosen == "api" {
			fields["speech"] = "api"
		}
		return a.ChangeSettings(fields)
	default:
		return errors.New("выберите провайдера: Groq или свой сервис")
	}
}

// applyProviderLink — вставить ссылку-конфигурацию.
func (a *App) applyProviderLink(raw string) error {
	link, err := providercfg.Parse(raw)
	if err != nil {
		return err
	}
	fields := map[string]any{
		"speech":       "api",
		"speech_url":   link.URL,
		"speech_model": link.Model,
	}
	if strings.TrimSpace(link.Key) == "" {
		// Ссылка без ключа значит «ключа нет», а не «оставь прежний»: иначе ключ
		// другого провайдера молча ушёл бы на новый адрес, и человек узнал бы
		// об этом по отказу сервиса. Прежнюю открытую копию убираем только при
		// удаче — при ошибке ChangeSettings файл не переписывается.
		fields["speech_key"] = ""
		return a.ChangeSettings(fields)
	}
	if err := a.applyLinkKey(link.URL, link.Key); err != nil {
		return err
	}
	return a.ChangeSettings(fields)
}

// applyLinkKey — ключ из ссылки в хранилище, ДО записи адреса и модели: не
// сошлось — провайдер остаётся прежним, а прежнее открытое место не стёрто.
//
// Ключ пишется по адресу, к которому человек пойдёт, а не по тому, что записан
// в настройках сейчас. Так адрес ссылки с пустым путём — это сам адрес Groq
// по умолчанию, и ключ ляжет под то же имя, под каким его потом спросят.
func (a *App) applyLinkKey(linkURL, key string) error {
	address := linkURL
	if strings.TrimSpace(linkURL) == "" {
		address = speech.GroqBaseURL
	}
	_, err := speech.SetKeyChecked(speech.ProvidersPath(), address, key)
	return err
}

// groqRegistered — что записано про Groq в providers.json (адрес и модель).
func groqRegistered() speech.Registered {
	found, _ := speech.RegisteredProvider(speech.ProvidersPath(), speech.GroqProviderID, nil)
	return found
}

// firstNonEmpty — первое непустое значение.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
