// Служба диктовки — распознавание на этой же машине, общее для всех клиентов.
//
// ЗАЧЕМ ЭТОТ СПОСОБ. Распознаватель у машины уже есть, и поднят он не нами:
// приложение «Диктовка» держит модель в памяти круглые сутки и отвечает всякому,
// кто постучится на его адрес. Держать рядом вторую копию модели — это ещё
// триста мегабайт памяти и ещё одна загрузка на каждую заметку. Поэтому заметки
// и диктовка виджета расшифровываются тем же распознавателем, что и диктовка в
// окне, — и качество у них одинаковое, сравнивать нечего.
//
// ПОЧЕМУ НЕ ПРОСТО ПО СЕТИ. Адрес службы — это 127.0.0.1: запись не уезжает с
// машины никуда. По порядку предпочтения этот способ идёт сразу после локальной
// модели и до всего, что отправляет звук наружу.
//
// ПОЧЕМУ РАЗГОВОРНЫЙ ПУТЬ, А НЕ ОДИН ЗАПРОС. Служба принимает запись кусками по
// двести миллисекунд и сама режет речь на фразы, а по знаку паузы заканчивает
// фразу и правит её моделью. Путь «положи файл целиком» упёрся бы в предел
// длины на длинной заметке, а побочная выгода разговорного — та же правка
// текста, что и у живой диктовки. Разговоров на одну заметку уходит столько,
// сколько нужно: разговор длиннее двух минут служба не переживает, поэтому звук
// делится по сто секунд, а тексты склеиваются в порядке звука.
//
// СЛУЖБУ ОТСЮДА НЕ ПОДНИМАЮТ. Она общая на машину: её заводит задача
// планировщика при включении, и поднятая нами копия жила бы в сеансе человека и
// умирала вместе с ним — а заметка, записанная за минуту до выхода из сеанса,
// осталась бы без текста.
package speech

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/appdirs"
)

const (
	// Частота и формат, в которых служба ждёт звук: 16 кГц, моно, 16 бит.
	dictationRate   = 16000
	dictationHeader = 44
	// Двести миллисекунд — кусок, с которым служба успевает разбирать речь и не
	// захлёбывается служебными запросами.
	dictationChunk = dictationRate * 2 / 5
	// Сколько звука уходит в один разговор со службой. Больше двух минут одного
	// разговора служба не переживает: на ста тридцати пяти секундах она отвечает
	// «разговор не найден», и сказанное пропадает — а те же секунды, разбитые на
	// два разговора, проходят целиком. Это её нутро, а не настройка; проверено на
	// живой службе, и сто секунд взяты с запасом.
	dictationTalk = 100 * dictationRate * 2
	// Пауза — тишина, после которой фраза считается законченной. Порог двойной:
	// абсолютный ловит молчание в тихой комнате, относительный — шум кулера,
	// который иначе не давал бы фразе закончиться. Потолок относительного нужен,
	// чтобы случайный кашель в начале не сделал «тишиной» всю оставшуюся речь.
	dictationFloor   = 200
	dictationShare   = 0.08
	dictationCeiling = 1200
	// Предел ожидания ответа на один запрос. Разбор получаса речи идёт минутами,
	// и прежний короткий предел обрывал бы его ровно там, где он длиннее всего.
	// Предел здесь только против зависшей службы.
	dictationWait = 10 * time.Minute
	// Кусок, который дробить уже некуда, и предел дробления. Разговор со службой
	// может пропасть посреди речи — движок перезапускает надзиратель, — и тогда
	// сказанное достаётся не целиком: кусок делится пополам и разбирается
	// заново. Десять секунд — та длина, с которой распознаватель и так
	// справляется, а четыре деления покрывают разговор в сто секунд.
	dictationSmallest = 10 * dictationRate * 2
	dictationSplits   = 4
)

// DictationService — распознавание общей службой диктовки.
type DictationService struct {
	// Endpoint — где искать сведения о службе. Пусто — обычные места: служебный
	// профиль, потом личная папка человека.
	Endpoint []string
	// Wait — сколько ждём ответа на один запрос.
	Wait time.Duration
	// HTTP — чем ходить. Пусто — обычный клиент.
	HTTP *http.Client
}

func NewDictationService() *DictationService { return &DictationService{} }

func (d *DictationService) Name() string { return "dictation" }

func (d *DictationService) Title() string { return "Служба диктовки" }

// Ready — есть ли о чём спрашивать.
//
// Проверка нарочно дешёвая: сведения на диске и живость номера процесса. Стучать
// в службу отсюда нельзя — Ready спрашивают при каждом выборе способа, и
// зависшая служба задерживала бы начало диктовки на свой предел ожидания.
func (d *DictationService) Ready() (bool, string) {
	if _, err := d.where(); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// where — где служба и чем подтверждаться.
//
// Сведений два, и порядок не случаен. Служба общая на машину, и поднятая от
// имени системы она оставляет сведения о себе в служебном профиле. В личной
// папке человека сведения остаются от прежних запусков — от движка, который
// поднимался в сеансе, — и адрес там мёртвый. Поэтому сперва служебный профиль;
// в личную папку смотрим только тогда, когда живого адреса нет нигде.
func (d *DictationService) where() (dictationWhere, error) {
	var seen *dictationWhere
	for _, path := range d.endpoints() {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var stored struct {
			URL   string `json:"url"`
			Token string `json:"token"`
			PID   int    `json:"pid"`
		}
		// Файл переписывается в момент запуска: половина строки — не поломка, а
		// повод посмотреть на вторые сведения.
		if json.Unmarshal(raw, &stored) != nil {
			continue
		}
		if stored.URL == "" || stored.Token == "" {
			continue
		}
		one := dictationWhere{URL: stored.URL, Token: stored.Token, PID: stored.PID}
		if seen == nil {
			seen = &one
		}
		if dictationAlive(stored.PID) {
			return one, nil
		}
	}
	if seen != nil {
		return *seen, nil
	}
	return dictationWhere{}, errors.New("сведений о ней нет — служба поднимается сама при включении машины, проверьте задачу «Dictation Service»")
}

// endpoints — где искать сведения о службе.
func (d *DictationService) endpoints() []string {
	if len(d.Endpoint) > 0 {
		return d.Endpoint
	}
	root := appdirs.LocalRoot()
	system := os.Getenv("SystemRoot")
	if system == "" {
		system = `C:\Windows`
	}
	return []string{
		filepath.Join(system, "System32", "config", "systemprofile", "AppData", "Local", "Dictation", "endpoint.json"),
		filepath.Join(root, "Dictation", "endpoint.json"),
	}
}

// dictationWhere — адрес службы и ключ к ней.
type dictationWhere struct {
	URL   string
	Token string
	PID   int
}

// Text — расшифровать файл службой диктовки.
func (d *DictationService) Text(ctx context.Context, path string) (string, error) {
	pcm, err := dictationPCM(path)
	if err != nil {
		return "", err
	}
	wait := d.Wait
	if wait <= 0 {
		wait = dictationWait
	}
	ask, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	// Звук идёт разговорами по сто секунд, а не одним на всю заметку: разговор
	// длиннее двух минут служба не переживает, и текст обрывался бы на второй
	// минуте — на длинной заметке это молча потерянный хвост.
	var parts []string
	for at := 0; at < len(pcm); at += dictationTalk {
		end := at + dictationTalk
		if end > len(pcm) {
			end = len(pcm)
		}
		one, err := d.talkPiece(ask, pcm[at:end], 0)
		if err != nil {
			return "", err
		}
		if one != "" {
			parts = append(parts, one)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " ")), nil
}

// talkPiece — разговор со службой с запасом на обрыв.
//
// Служба пропадает посреди речи не по нашей вине: её движок перезапускает
// надзиратель, и разговор уходит вместе с ним. Раньше один обрыв уносил всю
// расшифровку заметки — человек получал пустое поле и звук, который можно
// послушать, но некуда вставить. Звук у нас свой, поэтому кусок, на котором
// разговор оборвался, делится пополам и разбирается заново, пока не дойдёт до
// куска, который дробить уже некуда. Разобранное до обрыва не пропадает: если
// половины дали меньше целого куска, берём разобранное — оно начало той же
// речи, и лишнего в нём нет.
func (d *DictationService) talkPiece(ctx context.Context, pcm []byte, depth int) (string, error) {
	text, err := d.talk(ctx, pcm)
	if err == nil {
		return text, nil
	}
	if len(pcm) <= dictationSmallest || depth >= dictationSplits {
		if text != "" {
			return text, nil
		}
		return "", err
	}
	half := len(pcm) / 2
	half -= half % 2
	head, firstErr := d.talkPiece(ctx, pcm[:half], depth+1)
	tail, secondErr := d.talkPiece(ctx, pcm[half:], depth+1)
	if firstErr != nil && secondErr != nil {
		if text != "" {
			return text, nil
		}
		return "", secondErr
	}
	joined := strings.TrimSpace(strings.Join([]string{head, tail}, " "))
	if len(joined) >= len(strings.TrimSpace(text)) {
		return joined, nil
	}
	return strings.TrimSpace(text), nil
}

// talk — один разговор со службой: открыть, скормить звук кусками, закрыть.
func (d *DictationService) talk(ctx context.Context, pcm []byte) (string, error) {
	opened, err := d.ask(ctx, "/speech/start", map[string]any{"sample_rate": dictationRate})
	if err != nil {
		return "", err
	}
	id, _ := opened["id"].(string)
	if id == "" {
		return "", errors.New("служба диктовки не открыла разговор")
	}

	text := ""
	loudest, quiet := 0.0, false
	for at := 0; at < len(pcm); at += dictationChunk {
		end := at + dictationChunk
		if end > len(pcm) {
			end = len(pcm)
		}
		piece := pcm[at:end]
		level := dictationLoudness(piece)
		if level > loudest {
			loudest = level
		}
		silent := level < math.Max(dictationFloor, math.Min(loudest*dictationShare, dictationCeiling))
		answer, err := d.ask(ctx, "/speech/feed", map[string]any{
			"id":    id,
			"pcm":   base64.StdEncoding.EncodeToString(piece),
			"pause": silent && !quiet,
		})
		if err != nil {
			// Разговор не бросаем: служба держит их считанные единицы, и
			// забытый занял бы место живой диктовки до самого устаревания.
			d.forget(id)
			// Разобранное отдаём вместе с ошибкой: обрыв уносит разговор, а не
			// сказанное до него, и решать, что с этим делать, — не здесь
			// (см. talkPiece).
			return text, err
		}
		quiet = silent
		if one, _ := answer["text"].(string); one != "" {
			text = one
		}
	}

	// Последний кусок служба доразбирает как конец фразы — здесь и появляется
	// всё, что не успело устояться.
	last, err := d.ask(ctx, "/speech/stop", map[string]any{"id": id})
	if err != nil {
		// Здесь тоже отдаём разобранное: разговор уже закрыт, и терять его
		// слова из-за отказа на последнем шаге незачем.
		return text, err
	}
	if one, _ := last["text"].(string); one != "" {
		text = one
	}
	return strings.TrimSpace(text), nil
}

// ask — один запрос к службе.
//
// Ошибка ответа отделена от «не достучались» нарочно: по ней решается, повторять
// ли запрос по свежему адресу. Служба, перезапустившись, занимает другой порт, и
// запомненный адрес повёл бы запрос в пустоту — а выглядело бы это как «диктовка
// сломалась».
func (d *DictationService) ask(ctx context.Context, path string, body map[string]any) (map[string]any, error) {
	where, err := d.where()
	if err != nil {
		return nil, fmt.Errorf("служба диктовки не отвечает: %w", err)
	}
	answer, err := d.once(ctx, where, path, body)
	if err == nil || errors.Is(err, errDictationAnswered) {
		return answer, err
	}
	fresh, again := d.where()
	if again != nil || fresh.URL == where.URL {
		return nil, err
	}
	return d.once(ctx, fresh, path, body)
}

// errDictationAnswered — служба ответила (пусть и отказом): адрес верный, и
// повторять по нему нечего.
var errDictationAnswered = errors.New("служба диктовки ответила")

func (d *DictationService) once(ctx context.Context, where dictationWhere, path string, body map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	address := strings.TrimSuffix(where.URL, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	// Заголовка Origin здесь нет намеренно: с ним служба отвергает запрос — так
	// она защищается от страниц в браузере.
	request.Header.Set("Authorization", "Bearer "+where.Token)
	request.Header.Set("Content-Type", "application/json")

	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: dictationWait}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("служба диктовки не отвечает: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))

	switch {
	case response.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: не приняла ключ (она перезапускалась?)", errDictationAnswered)
	case response.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: старой версии, разговорной диктовки не знает", errDictationAnswered)
	case response.StatusCode < 200 || response.StatusCode > 299:
		return nil, fmt.Errorf("%w (%d): %s", errDictationAnswered, response.StatusCode, strings.TrimSpace(string(answer)))
	}

	var parsed map[string]any
	if err := json.Unmarshal(answer, &parsed); err != nil {
		return nil, fmt.Errorf("%w: ответ неразборчив", errDictationAnswered)
	}
	return parsed, nil
}

// forget — попросить службу закрыть разговор, который мы бросаем.
func (d *DictationService) forget(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	where, err := d.where()
	if err != nil {
		return
	}
	_, _ = d.once(ctx, where, "/speech/stop", map[string]any{"id": id})
}

// dictationPCM — тело записи без заголовка, с проверкой, что это та самая запись.
//
// Проверка стоит две строки, а молчаливо разобранный не тот звук стоит человеку
// заметки: служба ждёт 16 кГц моно, и запись в другом формате превратилась бы в
// ровный шум.
func dictationPCM(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("запись не прочитать: %w", err)
	}
	if len(raw) < dictationHeader+2 ||
		string(raw[0:4]) != "RIFF" ||
		string(raw[8:12]) != "WAVE" ||
		binary.LittleEndian.Uint16(raw[22:]) != 1 ||
		binary.LittleEndian.Uint32(raw[24:]) != dictationRate ||
		binary.LittleEndian.Uint16(raw[34:]) != 16 {
		return nil, errors.New("запись не того формата: нужен WAV 16 кГц, моно, 16 бит")
	}
	return raw[dictationHeader:], nil
}

// dictationLoudness — насколько громок кусок: средний квадрат по отсчётам.
func dictationLoudness(pcm []byte) float64 {
	sum, count := 0.0, 0
	for at := 0; at+1 < len(pcm); at += 2 {
		sample := float64(int16(binary.LittleEndian.Uint16(pcm[at:])))
		sum += sample * sample
		count++
	}
	if count == 0 {
		return 0
	}
	return math.Sqrt(sum / float64(count))
}
