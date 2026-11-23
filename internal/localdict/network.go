// Распознавание по сети — единственный способ диктовки.
//
// ЗАЧЕМ ЭТО В СЛУЖБЕ. Значок выбирает способ сам, но чужие окна — плагин
// диктовки в агентской оболочке — присылают запись службе и слов ждут от неё:
// решение, чем разбирать, должно быть одно на всю программу.
//
// ОТКУДА ВЫБОР. Из voice.json, из того же поля speech, что читает значок: выбор
// способа один на всю диктовку, и второй его копии быть не должно.
//
// МЕСТНЫХ МОДЕЛЕЙ БОЛЬШЕ НЕТ. Ни «auto», ни
// пустое поле, ни прежние «local»/«gigaam» не значат модель на машине: такие
// настройки молча означают облако. Если при них был вписан свой адрес, он
// остаётся главнее общего умолчания — человек уже сказал, куда ходить.
//
// ЧТО ПРИ ОТКАЗЕ. Нет ключа, кончился предел, отвалилась сеть — местной подмены
// нет: причина ложится в журнал, запись остаётся на диске, а фраза встаёт в
// очередь повтора (retry.go). Пустое поле вместо слов человек не простит, а
// причину найдёт в журнале и в окне настроек диктовки.
package localdict

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	core "github.com/zZZTeJleTTy3uKZZz/dictation/third_party/speech"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/providercfg"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
)

// errCloudUnavailable — облако не ответило, а другого пути у диктовки нет.
var errCloudUnavailable = errors.New("Облако не ответило.")

// cloudSpeechChosen — диктовка идёт по сети. Местного пути больше нет (решение
// владельца), поэтому «нет» бывает только у своего сервиса без адреса: идти
// некуда. Прежние «gigaam», «local» и «auto» означали выбор местной модели;
// теперь такие настройки молча означают облако.
func cloudSpeechChosen(voice *prefs.Voice) bool {
	if voice == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(voice.Speech)) {
	case "api":
		return strings.TrimSpace(voice.SpeechURL) != ""
	}
	return true
}

// defaultSpeechLanguage — язык диктовки, когда человек его не назвал. Диктовка
// русская, и в запрос он идёт явно: см. cloudOffers.
const defaultSpeechLanguage = "ru"

// cloudOffers — облачные способы в порядке предпочтения. Он один: Groq отвечает
// за секунду и слышит имена верно. Запасного адреса с другим ключом больше нет:
// второй сервис отвечал медленнее, а фразу при отказе и так
// сохраняет история с автоповтором.
func cloudOffers(voice *prefs.Voice, onWait func(time.Duration, int), client *http.Client) []speech.Channel {
	language := ""
	if voice != nil {
		language = strings.TrimSpace(voice.SpeechLanguage)
	}
	// Язык не назван — диктуем по-русски. Замер на одной записи это и показал: без
	// поля language Groq пишет имена латиницей («Atlas» вместо «Атлас») и теряет
	// подсказку-словарь, а с ним — те самые «Лидоруб» и «дэшборд», ради которых
	// словарь и заведён. Сказанное человеком в настройках остаётся главнее.
	if language == "" {
		language = defaultSpeechLanguage
	}
	offers := []speech.Channel{
		// Явно выбранную модель отдаём способу; не выбранную — пусть возьмёт
		// запись провайдера (на этой машине это может быть прямой Groq).
		speech.NewGroq(explicitSpeechModel(voice), language),
	}
	// Ожидание лимита должно быть слышно: иначе человек, отпустив клавишу, видит
	// пустое поле и не знает, что служба ждёт, а не сломалась.
	for _, one := range offers {
		if cloud, ok := one.(*speech.Cloud); ok {
			cloud.OnWait = onWait
			cloud.HTTP = client
		}
	}
	return offers
}

// cloudChannel — облачный способ, которым разбирать запись.
//
// Выбранный способ главнее порядка: человек мог назвать своего провайдера, и
// подменять его Groq только потому, что тот первый в списке, значило бы не
// слышать выбора. Неготовый выбранный способ подменяется рабочим — и об этом
// пишется в журнал.
func cloudChannel(voice *prefs.Voice, onWait func(time.Duration, int), onLog func(string, string), client *http.Client) speech.Channel {
	chosen := ""
	if voice != nil {
		chosen = strings.ToLower(strings.TrimSpace(voice.Speech))
	}
	offered := cloudOffers(voice, onWait, client)
	switch chosen {
	case "groq":
		// Выбранный облачный способ пробуется первым, остальные — подменой.
		if pick := named(offered, chosen); pick != nil {
			first := []speech.Channel{pick}
			for _, one := range offered {
				if one != pick {
					first = append(first, one)
				}
			}
			offered = first
		}
	case "api":
		// Общий сетевой сервис: адрес и ключ человек вписал сам, и знать их
		// больше неоткуда.
		offered = append([]speech.Channel{genericNetwork(voice, onWait, client)}, offered...)
	default:
		// Всё остальное — и пустое поле, и старый выбор местной модели — значит
		// «человек ничего сетевого не называл». Местных моделей в диктовке
		// больше нет (решение владельца), поэтому старые настройки молча
		// переходят на облако: разбирать запись всё равно надо, и сделать это
		// может только оно. Вписанный раньше свой адрес остаётся главнее
		// общего умолчания: человек уже сказал, куда ходить.
		if voice == nil || strings.TrimSpace(voice.SpeechURL) == "" {
			break
		}
		if one := genericNetwork(voice, onWait, client); one != nil {
			offered = append([]speech.Channel{one}, offered...)
		}
	}
	return readyCloud(offered, chosen, onLog)
}

func (a *App) networkChannel() speech.Channel {
	return cloudChannel(a.voice(), a.speechWaitLine, a.sessionLog, a.recognitionClient())
}

func (a *App) networkChannelForDictation() speech.Channel {
	a.mu.Lock()
	phrase := a.phraseChannel
	a.mu.Unlock()
	if phrase != nil {
		// Фраза уже началась: её язык и ключ не меняет смена настроек.
		// Повтор этой же фразы тоже идёт её способом.
		return phrase
	}
	return a.networkChannel()
}

// dictationSpeechFor — чем служба разбирает диктовку: имя способа и слова для
// человека.
//
// Спрашивается на каждый показ состояния, поэтому молчит: писать в журнал на
// каждый взгляд окна нельзя — журнал от этого превратится в шум, а по нему
// читают живую диктовку.
func dictationSpeechFor(voice *prefs.Voice, channel speech.Channel) (string, string) {
	if channel != nil {
		return channel.Name(), "«" + channel.Title() + "»"
	}
	// Способ не собран: нет ключа или адреса. Подписываем тем, которым служба
	// попробует разобрать, как только ключ появится, — по подписи видно, куда
	// его вписывать, — и говорим, что он пока не готов.
	if intended := intendedCloud(voice); intended != nil {
		return intended.Name(), "«" + intended.Title() + "» пока не готов: проверьте ключ"
	}
	return "", "облачное распознавание не настроено: впишите ключ к сервису"
}

// readyCloud — первый готовый облачный способ из предложенных.
//
// Один и тот же разбор на два случая: и когда способ выбирается по настройкам, и
// когда местный выбор выведен и выбирать не из чего.
func readyCloud(offered []speech.Channel, chosen string, onLog func(string, string)) speech.Channel {
	for _, one := range offered {
		if one == nil {
			continue
		}
		if ready, why := one.Ready(); ready {
			return one
		} else if chosen == one.Name() && onLog != nil {
			onLog("speech-substituted", "chosen="+chosen+" reason="+why)
		}
	}
	return nil
}

// named — способ с этим именем из списка.
func named(offered []speech.Channel, name string) speech.Channel {
	for _, one := range offered {
		if one.Name() == name {
			return one
		}
	}
	return nil
}

// genericNetwork — способ «по сети» с настройками человека.
func genericNetwork(voice *prefs.Voice, onWait func(time.Duration, int), client *http.Client) speech.Channel {
	if voice == nil || strings.TrimSpace(voice.SpeechURL) == "" {
		return nil
	}
	where := speech.RemoteFromVoice(voice)
	where.OnWait = onWait
	where.HTTP = client
	return speech.NewOpenAICompatible(where)
}

// recognitionClient — клиент распознавания с прокси человека, если он задан.
//
// Ошибку адреса прокси не глотаем в пустоту: строку пишет журнал, а окно видит
// её в поле прокси. Иначе человек с опечаткой в адресе получал бы «нет связи» и
// искал бы её в интернете.
func (a *App) recognitionClient() *http.Client {
	client, _, why := speech.RecognitionClient(speech.ProvidersPath(), 0)
	if why != "" {
		a.sessionLog("proxy-ignored", why)
	}
	return client
}

// speechWaitLine — сказать в журнал, что мы ждём лимита. Ждать молча нельзя:
// человек уже отпустил клавишу и смотрит на пустое поле, и «ничего не
// происходит» он прочитает как поломку. Журнал диктовки читает окно настроек,
// так что строка доходит до человека.
func (a *App) speechWaitLine(wait time.Duration, attempt int) {
	a.sessionLog("speech-wait", fmt.Sprintf("wait_ms=%d attempt=%d", wait.Milliseconds(), attempt))
}

// cloudTrouble объясняет отказ облака человеку: коротко — для строки истории,
// полностью — для плашки и журнала. Причина называется по виду отказа, а не
// «распознавание не удалось»: человеку нужно знать, чинить ли интернет, ключ
// или ждать, пока сервис отпустит предел частоты.
func cloudTrouble(err error) (short, full string) {
	// Короткое имя отказа берётся из словаря ядра (speech.Verdict): те же слова
	// человек видит в окне проверки ключа. Здесь остаётся то, чего окно не
	// знает, — что запись сохранена и повтор придёт сам.
	if err == nil {
		return "облако не настроено", "Облако не настроено: не вписан ключ или адрес сервиса."
	}
	// Сервис ОТВЕТИЛ ошибкой (5xx и прочие ответы) — это не «нет связи»: сеть
	// человека тут ни при чём, и совет «проверьте интернет» увёл бы чинить не то.
	// Проверяется ДО вида: у кода ядра «недоступность» одна на 5xx и на срыв
	// сети, и по виду они неразличимы.
	var failed *core.Error
	if errors.As(err, &failed) && failed.Status >= 500 {
		return providercfg.Verdict(providercfg.KindServer).Reason,
			"Облако ответило ошибкой (" + shortReason(err) + "). Запись сохранена и будет повторена."
	}
	switch providercfg.KindOfCode(classifyRefusal(err)) {
	case providercfg.KindInvalidKey:
		return providercfg.Verdict(providercfg.KindInvalidKey).Reason,
			"Облако не приняло ключ. Проверьте ключ в настройках."
	case providercfg.KindBusy:
		return providercfg.Verdict(providercfg.KindBusy).Reason,
			"Облако просит подождать: слишком много запросов (429)."
	case providercfg.KindTimeout:
		// Тайм-аут — не «нет сети»: сеть есть, а сервис не успел. Человеку это
		// важно различать, потому что чинится оно ожиданием, а не роутером.
		return providercfg.Verdict(providercfg.KindTimeout).Reason,
			"Облако не ответило вовремя (тайм-аут). Запись сохранена и будет повторена."
	case providercfg.KindNetwork, providercfg.KindTLS:
		// До ответа не дошло (обрыв, DNS, таймаут, TLS): эту беду видит человек
		// своей сетью. Длительность разбирать не нужно: срок отказа ядро уже
		// назвало таймаутом выше.
		return providercfg.Verdict(providercfg.KindNetwork).Reason,
			"Облако не ответило: нет связи. Проверьте интернет — запись сохранена и будет повторена."
	default:
		return providercfg.Verdict(providercfg.KindRefused).Reason, "Облако отказало: " + shortReason(err)
	}
}

// classifyRefusal — код отказа ядра по ошибке: у клиента ядра он есть в самой
// ошибке, у прочих (сеть, отмена) его даёт общий разбор ядра.
func classifyRefusal(err error) core.Code {
	var failed *core.Error
	if errors.As(err, &failed) && failed.Code != "" {
		return failed.Code
	}
	return core.Classify(err, nil).Code
}

// isTimeout — запрос не уложился в срок.
func isTimeout(err error) bool {
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// refusalText — текст причины, если отказ пришёл словами разбора, а не от
// облака (cloudError). Подменный разбор в проверках и повторы отдают свои
// ошибки — у них уже есть понятная человеку причина.
func refusalText(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var cloud *cloudError
	if errors.Is(err, errCloudUnavailable) || errors.As(err, &cloud) {
		return "", false
	}
	return shortReason(err), true
}

// failureReason — причина отказа словами для строки истории.
//
// Строка истории читается человеком, поэтому отказ облака называется по виду
// (ключ, тайм-аут, нет сети, сервис отказал), а не «распознавание не удалось».
// Прочие ошибки остаются как есть: у них уже есть своя понятная причина, и
// подменять её общим «облако отказало» значило бы спрятать настоящую.
func failureReason(cause error) string {
	if cause == nil {
		return "распознавание не удалось"
	}
	if errors.Is(cause, errAudioNotFound) {
		return "файл записи повреждён или пропал"
	}
	var cloud *cloudError
	if errors.Is(cause, errCloudUnavailable) || errors.As(cause, &cloud) || classifyRefusal(cause) != "" {
		if short, _ := cloudTrouble(cause); short != "" {
			return short
		}
	}
	return shortReason(cause)
}

// cloudError — отказ облака с причиной. Причина остаётся в цепочке ошибок:
// по ней видно, повторять ли попытку (обрыв, 429) или она бессмысленна (ключ).
type cloudError struct {
	cause error
	full  string
}

func (e *cloudError) Error() string { return errCloudUnavailable.Error() + " " + e.full }

func (e *cloudError) Unwrap() []error {
	if e.cause == nil {
		return []error{errCloudUnavailable}
	}
	return []error{errCloudUnavailable, e.cause}
}

// cloudRefusal — ошибка облака с причиной, пригодная для errors.Is.
func cloudRefusal(err error) error {
	_, full := cloudTrouble(err)
	return &cloudError{cause: err, full: full}
}

// cloudProbeTimeout — срок лёгкого запроса к облаку. Это не разбор записи:
// вопрос «ты здесь?» должен получать ответ за секунды, иначе он сам станет
// помехой повтору.
const cloudProbeTimeout = 5 * time.Second

// cloudReachable — ответило ли облако на лёгкий запрос.
//
// Это то же, что readyCloud, но по-настоящему: readyCloud проверяет только
// настройки, а здесь мы спрашиваем сам сервис. Запрос берётся самый дешёвый —
// список моделей: он есть у Groq и у своего сервиса, потому что
// все они говорят на языке OpenAI. Заодно проверяется, что ключ на месте.
//
// Ходим ТЕМ ЖЕ клиентом, что и сама диктовка (a.recognitionClient): через
// прокси человека, если он вписан. Иначе из сети, где прямой запрос не доходит,
// проверка вечно отвечала бы «облако не здесь», автоповтор откладывался бы до
// бесконечности, и диктовка через прокси не повторялась бы ни разу —
// http.DefaultClient прокси этой программы не знает.
//
// Ответ «нет модели» или «нет такого пути» — это тоже ответ: значит сеть и
// сервис работают, и можно пробовать разбор. А вот 5xx и «слишком много
// запросов» — не ответ, а отказ: с ними повтор только жжёт время.
func (a *App) cloudReachable(ctx context.Context) bool {
	channel := a.networkChannelForDictation()
	if channel == nil {
		return false
	}
	return cloudProbe(ctx, channel, a.recognitionClient())
}

// cloudProbe — лёгкий вопрос «облако здесь?» к уже собранному способу.
//
// client — чем ходить; nil — клиент самого способа, а если и его нет, обычный
// клиент этой машины. Глобальный http.DefaultClient не подставляется никогда:
// его настройки — чужие, и прокси программы в нём нет.
func cloudProbe(ctx context.Context, channel speech.Channel, client *http.Client) bool {
	address, key, ok := probeTarget(channel)
	if !ok {
		return false
	}
	ask, cancel := context.WithTimeout(ctx, cloudProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ask, http.MethodGet, address, nil)
	if err != nil {
		return false
	}
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	if client == nil {
		client = probeClient(channel)
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	switch {
	case response.StatusCode >= 500:
		return false
	case response.StatusCode == http.StatusTooManyRequests:
		return false
	}
	return true
}

// probeClient — клиент, которым ходит сам способ; nil — знать не знаем.
func probeClient(channel speech.Channel) *http.Client {
	switch one := channel.(type) {
	case *speech.Cloud:
		return one.HTTP
	case *speech.OpenAICompatible:
		return one.HTTP
	}
	return &http.Client{Timeout: cloudProbeTimeout}
}

// probeTarget — адрес лёгкого запроса и ключ облачного способа. ok=false —
// способ не облачный (например, служба на этой же машине), и спрашивать некого.
func probeTarget(channel speech.Channel) (address, key string, ok bool) {
	switch one := channel.(type) {
	case *speech.Cloud:
		where, _, whereKey, why := one.Where()
		if why != "" {
			return "", "", false
		}
		return modelsURL(where), whereKey, true
	case *speech.OpenAICompatible:
		return modelsURL(one.URL), one.Key, true
	}
	return "", "", false
}

// modelsURL — адрес списка моделей из адреса расшифровки. Хвост
// /audio/transcriptions отрезается: он принадлежит разбору записи, а не
// проверке связи.
func modelsURL(address string) string {
	base := strings.TrimRight(strings.TrimSpace(address), "/")
	base = strings.TrimSuffix(base, "/audio/transcriptions")
	if base == "" {
		return ""
	}
	return base + "/models"
}

// shortReasonOrNone — причина для строки журнала; nil — «нет причины».
func shortReasonOrNone(err error) string {
	if err == nil {
		return "none"
	}
	return shortReason(err)
}

// shortReason — причина отказа одной строкой для журнала.
//
// Тело ответа бывает длинным, а журнал читает человек: строка обрезается по
// знакам, а не по байтам, чтобы русский текст не разорвался посередине буквы.
// Переводы строк сводятся к пробелу — иначе одна причина заняла бы в журнале
// несколько строк и сломала бы разбор.
func shortReason(err error) string {
	flat := strings.Join(strings.Fields(err.Error()), " ")
	runes := []rune(flat)
	const limit = 300
	if len(runes) > limit {
		flat = string(runes[:limit]) + "…"
	}
	if flat == "" {
		return "причина не названа"
	}
	return flat
}

// errCloudEmpty — сервис ответил, но слов в ответе нет. Не отказ связи: повторять
// такой ответ незачем, он повторится и вернёт то же самое.
var errCloudEmpty = errors.New("сервис распознавания вернул пустой ответ")

// networkText — разобрать запись облачным способом. ok=false — разбирать местной
// моделью.
func (a *App) networkText(ctx context.Context, audio string) (Recognition, bool) {
	result, err, attempted := a.networkAttempt(ctx, audio)
	if !attempted || err != nil {
		return Recognition{}, false
	}
	return result, true
}

// networkAttempt — один разбор облачным способом вместе с причиной отказа.
//
// Причина нужна тому, кто решает, повторять ли разбор: обрыв и таймаут проходят
// сами, а неверный ключ не пройдёт никогда. attempted=false — облачный способ
// сейчас не собран, и идти некуда.
func (a *App) networkAttempt(ctx context.Context, audio string) (Recognition, error, bool) {
	return a.networkAttemptWithPrompt(ctx, audio, "")
}

// networkAttemptWithPrompt — тот же разбор, но с хвостом предыдущего текста:
// он держит связность кусков, когда диктовка разбирается не целиком, а по
// частям.
func (a *App) networkAttemptWithPrompt(ctx context.Context, audio, prompt string) (Recognition, error, bool) {
	channel := a.networkChannelForDictation()
	if channel == nil {
		return Recognition{}, nil, false
	}
	began := time.Now()
	text, err := channelText(ctx, channel, audio, prompt)
	passed := time.Since(began).Milliseconds()
	if err != nil {
		// Ответ сервиса в журнал, а не ключ: по нему видно, чего именно он не
		// принял, — и он же остаётся единственным следом отказа. Без причины
		// строка бесполезна: «failed=true» не отличает предел частоты от
		// неподнятой сети, и человеку приходится спрашивать, что случилось.
		a.sessionLog("speech-substituted", fmt.Sprintf(
			"chosen=%s failed=true elapsed_ms=%d reason=%s",
			channel.Name(), passed, shortReason(err)))
		return Recognition{}, err, true
	}
	if strings.TrimSpace(text) == "" {
		a.sessionLog("speech-substituted", fmt.Sprintf("chosen=%s empty=true elapsed_ms=%d", channel.Name(), passed))
		return Recognition{}, errCloudEmpty, true
	}
	a.sessionLog("speech-cloud", fmt.Sprintf("chosen=%s elapsed_ms=%d", channel.Name(), passed))
	return Recognition{
		ID:      channel.Name(),
		Name:    channel.Title(),
		Text:    strings.TrimSpace(text),
		Seconds: time.Since(began).Seconds(),
	}, nil, true
}

// channelText — разбор записи способом. Хвост предыдущего текста уходит
// подсказкой только тем способам, которые её принимают: чужие сервисы,
// подражающие OpenAI, знают не все его поля, и лишнее поле обернулось бы
// отказом вместо текста. Пустой хвост — обычный разбор, без обещаний.
func channelText(ctx context.Context, channel speech.Channel, path, prompt string) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return channel.Text(ctx, path)
	}
	if prompted, ok := channel.(interface {
		TextWithPrompt(context.Context, string, string) (string, error)
	}); ok {
		return prompted.TextWithPrompt(ctx, path, prompt)
	}
	return channel.Text(ctx, path)
}

// cloudSpeech разбирает разговор облачным способом: запись дописывается и уходит
// в запрос целиком.
//
// Зовётся с запертым разговором: пока идёт запрос, присылать в него больше нечего
// — разговор закончен, и второй такой же запрос был бы платной ошибкой.
func (a *App) cloudSpeech(ctx context.Context, session *speechSession) error {
	if session.recorder == nil {
		return fmt.Errorf("запись разговора не открыта")
	}
	// Запись закрывается до отправки: сервис читает файл сам и незакрытую
	// страницу может не увидеть. Закрывать дважды нельзя, поэтому путь и длина
	// остаются разговору — их возьмёт finishSpeech.
	closed, seconds := a.closeSpeechAudio(session)
	if closed == "" {
		return fmt.Errorf("запись разговора пуста")
	}
	// Сжимать перед отправкой больше нечего: запись велась сразу в Ogg/Opus,
	// сорок килобит в секунду. Прежде здесь стояла упаковка WAV помощником
	// Windows — три четверти секунды ожидания после каждого разговора.
	session.savedPath, session.savedSeconds = closed, seconds
	path := closed

	began := time.Now()
	text, err := session.cloud.Text(ctx, path)
	passed := time.Since(began).Milliseconds()
	if err != nil || strings.TrimSpace(text) == "" {
		// Подмены местной моделью здесь больше нет: человек выбрал облако, и
		// разбирать сказанное чужой моделью значило бы обойти его выбор молча.
		// Отказ называется причиной, звук остаётся в записи разговора, а текст
		// можно получить повтором.
		a.sessionLog("speech-substituted", fmt.Sprintf("chosen=%s failed=%t empty=%t elapsed_ms=%d reason=%s",
			session.cloud.Name(), err != nil, err == nil, passed, shortReasonOrNone(err)))
		if err != nil {
			return cloudRefusal(err)
		}
		return cloudRefusal(errCloudEmpty)
	}
	a.sessionLog("speech-cloud", fmt.Sprintf("chosen=%s elapsed_ms=%d", session.cloud.Name(), passed))
	session.transcript = strings.TrimSpace(text)
	return nil
}

// cloudKey — адрес, модель, ключ и подсказка-словарь облачного способа.
//
// Просит их чужая программа, которая ходит в облако сама, минуя службу: так
// диктовка не ждёт ни прогрева модели, ни вставки через помощник поля. Ключ на
// диске лежит защищённым, и прочитать его может только наша половина — второй
// копии ключа открытым текстом не будет.
func (a *App) cloudKey(provider string) (map[string]any, string) {
	var channel *speech.Cloud
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "groq":
		channel = speech.NewGroq(explicitSpeechModel(a.voice()), "")
	default:
		return nil, "неизвестный способ распознавания: " + provider
	}
	address, model, key, why := channel.Where()
	if why != "" {
		return nil, why
	}
	return map[string]any{
		"provider": channel.Name(),
		"url":      address,
		"model":    model,
		"key":      key,
		"prompt":   channel.Prompt(),
	}, ""
}
