package localdict

// provider — откуда диктовка берёт текст: вид способа, подпись для человека,
// модель и узел.
//
// ЗАЧЕМ В ЯДРЕ, А НЕ В ОКНЕ. Новое окно показывает рядом с диктовкой, куда
// уходит звук: «Groq напрямую · whisper-large-v3». Окну для этого нужен не
// только адрес из voice.json, но и решение, каким способом служба разбирает
// сейчас: выбранный способ может быть не готов, и подменён рабочим. Второй
// такой расчёт в окне разошёлся бы с первым — это уже было со списком Teams.
// Поэтому поле собирает служба, а окно показывает готовое.
//
// ПОДПИСИ ЖИВУТ ЗДЕСЬ. Вид определяется по узлу: api.groq.com — прямой Groq,
// любой другой адрес — свой сервис. Наших адресов в умолчаниях больше нет:
// по умолчанию провайдера не задают, а человек выбирает его сам
// на первом запуске.

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
)

// Виды способа распознавания. Именно эти имена ждёт окно.
const (
	providerGroq  = "groq"
	providerCloud = "cloud"
)

// ProviderState — поле provider кадра состояния.
type ProviderState struct {
	Kind string `json:"kind"`
	// Title — подпись для человека: окно её не выдумывает.
	Title string `json:"title"`
	Model string `json:"model"`
	// Host — узел облака. Пусто — адрес не удалось разобрать.
	Host string `json:"host,omitempty"`
}

// provider собирает поле для кадра состояния. Спрашивает тот же выбор, которым
// разбирается запись, но молча: кадр состояния приходит по нескольку раз в
// секунду, и запись в журнал о подмене способа на каждый взгляд окна была бы
// шумом.
func (a *App) provider(voice *prefs.Voice) ProviderState {
	return providerFor(voice, providerChannel(voice, a.recognitionClient()))
}

// providerChannel собирает способ молча: кадр состояния приходит по нескольку
// раз в секунду, и запись в журнал о подмене способа на каждый взгляд окна была
// бы шумом.
func providerChannel(voice *prefs.Voice, client *http.Client) speech.Channel {
	return cloudChannel(voice, nil, nil, client)
}

// providerFor — поле provider по готовому облачному способу. nil означает, что
// ни один способ не готов: тогда называется тот, которым служба попробует
// разобрать.
func providerFor(voice *prefs.Voice, channel speech.Channel) ProviderState {
	if channel == nil {
		// Облако выбрано, но ни один способ не готов (нет ключа). Называем тот,
		// которым служба попробует разобрать: по виду и узлу человеку видно,
		// куда вписывать ключ.
		channel = intendedCloud(voice)
	}
	if channel == nil {
		return ProviderState{Kind: providerCloud, Title: "Облако", Model: speechModelOf(voice)}
	}
	endpoint, channelTitle := describeCloud(channel)
	kind, title := classifyProvider(hostOf(endpoint.url), channelTitle)
	result := ProviderState{Kind: kind, Title: title, Model: endpoint.model, Host: hostOf(endpoint.url)}
	if result.Model == "" {
		result.Model = speechModelOf(voice)
	}
	return result
}

// intendedCloud — способ, который был бы выбран, будь он готов. Порядок тот же,
// что у cloudChannel: названный человеком, иначе первый в списке предложений.
func intendedCloud(voice *prefs.Voice) speech.Channel {
	name := ""
	if voice != nil {
		name = strings.ToLower(strings.TrimSpace(voice.Speech))
	}
	// Свой адрес главнее общего списка — и когда он назван способом «api», и
	// когда остался от прежнего местного выбора.
	ownAddress := name == "api" || (name != "groq" && voice != nil && strings.TrimSpace(voice.SpeechURL) != "")
	if ownAddress {
		if one := genericNetwork(voice, nil, nil); one != nil {
			return one
		}
	}
	offered := cloudOffers(voice, nil, nil)
	if one := named(offered, name); one != nil {
		return one
	}
	if len(offered) > 0 {
		return offered[0]
	}
	return nil
}

// endpointOf — адрес и модель способа. Ключ не возвращается: он нужен только
// тому, кто идёт в сервис, а не тому, кто показывает подпись.
type endpointOf struct {
	url, model string
}

// describeCloud — адрес, модель и подпись способа. У способа, который не успел
// собрать запрос (нет ключа), берутся адрес и модель по умолчанию: по ним видно,
// куда человеку вписывать ключ.
func describeCloud(channel speech.Channel) (endpointOf, string) {
	switch cloud := channel.(type) {
	case *speech.Cloud:
		address, model, _, why := cloud.Where()
		if why != "" || address == "" {
			// Способ не готов — Where не собрал запрос. Называем адрес и модель
			// по умолчанию: по ним видно, куда человеку вписывать ключ.
			address = firstFilled(cloud.URL, cloud.FallbackURL)
			model = firstFilled(model, cloud.Model, cloud.FallbackModel)
		}
		return endpointOf{url: address, model: model}, cloud.Title()
	case *speech.OpenAICompatible:
		return endpointOf{url: cloud.URL, model: cloud.Model}, cloud.Title()
	default:
		return endpointOf{}, channel.Title()
	}
}

// classifyProvider — вид и подпись по узлу облака.
func classifyProvider(host, channelTitle string) (string, string) {
	switch {
	case host == "api.groq.com" || strings.HasSuffix(host, ".groq.com"):
		return providerGroq, "Groq напрямую"
	case channelTitle != "":
		return providerCloud, channelTitle
	default:
		return providerCloud, "Облако"
	}
}

// hostOf — узел из адреса. Адрес может быть записан и без схемы: человек
// вписывает его руками, и «speech.example.net/v1» — обычная запись.
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// speechModelOf — модель из настроек, когда способ не назвал свою.
func speechModelOf(voice *prefs.Voice) string {
	if voice == nil {
		return ""
	}
	return strings.TrimSpace(voice.SpeechModel)
}

func firstFilled(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
