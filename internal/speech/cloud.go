// Облачное распознавание — чужой сервис по сети, напрямую и по ключу с этой же
// машины.
//
// ПОЧЕМУ ОНО. Локальные модели терминов человека не лечат: замеры на одной и той
// же записи дали у них «лидоруб» строчными, а Groq с
// моделью whisper-large-v3 и подсказкой-словарём разобрал ту же речь за две
// секунды, с заглавными буквами, знаками и разбивкой на предложения.
//
// ПОЧЕМУ ОДИН СЕРВИС. Прежний запасной облачный сервис убран: он
// отвечал медленнее, стоил второго ключа и в выборе окна был лишним пунктом.
// Отказ облака фразу не теряет — она ждёт автоповтора в истории, — а кому нужен
// другой сервис, тот вписывает своего провайдера (openai-совместимый адрес).
//
// ПОЧЕМУ БЕЗ ПРОКСИ. Ключ лежит на этой же машине защищённым (providers.json), и
// посредник между записью и сервисом — это ещё одна сторона, которой видно и
// звук, и ключ. Запрос идёт прямо на адрес сервиса.
//
// ПОЧЕМУ ПОДСКАЗКА. Без неё та же модель пишет «лидеруб» строчными и без знаков:
// словарь терминов человека едет в запросе полем prompt (см. Glossary).
package speech

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Адреса и модели облачных сервисов. Это значения по умолчанию, а не настройка:
// они записываются в providers.json при первой же правке ключа, и дальше человек
// волен их поменять. Модель turbo дешевле и быстрее, но имена пишет строчными;
// large-v3 с подсказкой разбирает их верно.
const (
	// GroqProviderID — по этому имени запись Groq и находится.
	GroqProviderID = "groq-speech"
	// GroqBaseURL — адрес API Groq; запрос идёт на /audio/transcriptions от него.
	GroqBaseURL = "https://api.groq.com/openai/v1"
	// GroqModel — та модель, которой замеры и делались.
	GroqModel = "whisper-large-v3"

	groqName        = "Groq (распознавание речи)"
	transcriptions  = "/audio/transcriptions"
	cloudKeyTimeout = 3 * time.Minute
)

// Cloud — распознавание у облачного сервиса.
type Cloud struct {
	// ID — имя способа: им способ выбирают в настройках.
	ID string
	// Label — как способ называется для человека.
	Label string
	// ProviderID — запись в providers.json, откуда берутся ключ, адрес и модель.
	ProviderID string
	// Hosts — по каким узлам узнаётся запись человека, если своей у сервиса нет.
	Hosts []string
	// FallbackURL и FallbackModel — обычные адрес и модель сервиса.
	FallbackURL, FallbackModel string

	// Key, URL, Model — сказанное человеком в настройках; пусто — берём из записи.
	Key   string
	URL   string
	Model string
	// Language — язык речи; пусто — русский.
	Language string
	// Terms — словарь терминов для подсказки; nil — обычное место.
	Terms *Glossary
	// Wait — сколько ждём ответа. Запись в две минуты разбирается за секунды, и
	// предел здесь только против зависшего сервиса.
	Wait time.Duration
	// Path — где providers.json; пусто — обычное место службы диктовки.
	Path string
	// OnWait — кому сказать, что мы ждём лимита сервиса. Зовётся перед каждой
	// задержкой: сколько ждать и какая это попытка. Ждать молча нельзя: человек
	// уже отпустил клавишу и смотрит на пустое поле, и «ничего не происходит» он
	// прочитает как поломку.
	OnWait func(wait time.Duration, attempt int)
	// HTTP — чем ходить. Пусто — обычный клиент; задано — с прокси человека.
	HTTP *http.Client
}

// NewGroq — быстрый облачный способ.
func NewGroq(model, language string) *Cloud {
	return &Cloud{
		ID: "groq", Label: "Groq по сети",
		ProviderID:  GroqProviderID,
		FallbackURL: GroqBaseURL, FallbackModel: GroqModel,
		Model: model, Language: language, Wait: cloudKeyTimeout, Terms: NewGlossary(""),
	}
}

func (c *Cloud) Name() string  { return c.ID }
func (c *Cloud) Title() string { return c.Label }

func (c *Cloud) Ready() (bool, string) {
	if _, why := c.where(); why != "" {
		return false, why
	}
	return true, ""
}

// where — собрать запрос: адрес, ключ, модель и подсказку.
//
// Записанное в providers.json главнее обычных значений, а сказанное человеком в
// настройках — главнее записанного: адрес и модель он волен поменять, и тогда его
// правка должна работать, а не спорить с записью.
func (c *Cloud) where() (Remote, string) {
	registered, found := RegisteredProvider(c.Path, c.ProviderID, c.Hosts)
	key := c.Key
	base := c.FallbackURL
	if found && registered.BaseURL != "" {
		base = registered.BaseURL
	}
	if key == "" {
		// Ключ — тот, что принадлежит этому адресу: ключ нашего роутера не
		// уходит чужому провайдеру, а ключ провайдера не уходит нам.
		key = KeyForURL(c.Path, base).Key
	}
	if key == "" {
		return Remote{}, KeyForURL(c.Path, base).Why
	}
	if ready, why := readyFor(base, key); !ready {
		return Remote{}, why
	}
	where := Remote{
		Key: c.Key, Model: c.Model, Language: c.Language, Wait: c.Wait,
		OnWait: c.OnWait, HTTP: c.HTTP,
	}
	if where.Model == "" && found {
		where.Model = registered.Model
	}
	if where.Model == "" {
		where.Model = c.FallbackModel
	}
	where.URL = transcriptionsURL(c.URL, registered.BaseURL, c.FallbackURL)
	return where, ""
}

// transcriptionsURL — адрес запроса из адреса API.
//
// Свой адрес, а не чужой из настроек: расшифровка «по сети» умеет ходить куда
// угодно, и оставленный ею адрес увёл бы запрос на другой сервер — вместе с
// ключом.
func transcriptionsURL(own, registered, fallback string) string {
	base := strings.TrimRight(strings.TrimSpace(own), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(registered), "/")
	}
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(fallback), "/")
	}
	if strings.HasSuffix(base, transcriptions) {
		return base
	}
	return base + transcriptions
}

func (c *Cloud) Text(ctx context.Context, path string) (string, error) {
	return c.TextWithPrompt(ctx, path, "")
}

// TextWithPrompt — разбор с хвостом предыдущего текста в подсказке.
//
// Подсказка-словарь остаётся на месте: без неё имена пишутся строчными и вкривь.
// Хвост идёт следом и держит связность кусков: распознаватель видит, на чём
// кончился предыдущий, и не начинает новый будто с чистого листа.
func (c *Cloud) TextWithPrompt(ctx context.Context, path, tail string) (string, error) {
	where, why := c.where()
	if why != "" {
		return "", errors.New(why)
	}
	where.Prompt = PromptWithContext(c.terms().Hint(), tail)
	return NewOpenAICompatible(where).Text(ctx, path)
}

// Where — адрес запроса, модель и ключ.
//
// Нужно тому, кто ходит в облако сам, минуя нас: плагину диктовки в агентской
// оболочке ключ нужен на его стороне, а ключ на диске лежит защищённым, и прочитать
// его может только наша половина.
func (c *Cloud) Where() (string, string, string, string) {
	where, why := c.where()
	if why != "" {
		return "", "", "", why
	}
	return where.URL, where.Model, where.Key, ""
}

// Prompt — подсказка-словарь, как она уходит в запрос.
//
// Собирается здесь, а не в чужом окне: правило у неё одно (см. Glossary), и
// второй его список разошёлся бы с первым на первой же правке словаря.
func (c *Cloud) Prompt() string { return c.terms().Hint() }

// terms — словарь терминов: свой, если задан, иначе обычное место.
//
// Новый словарь на вызов, а не запомненный в поле: поле пришлось бы менять на
// ходу, а по способу ходят сразу несколько нитей — выбор способа, меню значка и
// сама заметка. Кэш живёт внутри словаря, и теряется только чтение файла.
func (c *Cloud) terms() *Glossary {
	if c.Terms != nil {
		return c.Terms
	}
	return NewGlossary(c.Path)
}

// hostOf — узел адреса: по нему запись человека и узнаётся.
func hostOf(address string) string {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}
