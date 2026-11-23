package localdict

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/aacrecover"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hotkey"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/updates"
	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/mediapause"
)

const Version = "1.13.2"

// freeOSMemory возвращает память системе. Отдельная переменная, а не прямой
// вызов: проверка должна видеть, что возврат был, не измеряя память процесса.
var freeOSMemory = debug.FreeOSMemory

// Ctrl+Пробел — сочетание по умолчанию. Прежнее Ctrl+D отдано окну, в котором
// человек работает: в агентских оболочках это сочетание запускает диктовку в
// самом поле ввода, и наше глобальное сочетание перехватывало бы его у страницы
// — Windows отдаёт зарегистрированную горячую клавишу нам и до окна её не
// доносит. Пробел с Ctrl в Windows не занят; если у человека он переключает
// раскладку или IME, сочетание меняют в настройках — запасное Ctrl+Alt+Space.
const defaultHotkey = prefs.DefaultHotkey

type Config struct {
	Hotkey string `json:"hotkey"`
	// Режима записи больше нет: сочетание работает только как «нажал —
	// говоришь — нажал». Прежний файл с полем mode читается как обычный:
	// незнакомое поле разбор пропускает, и сохранённое удержание молча
	// становится переключателем.
	IdleSeconds int `json:"idle_seconds"`
	// AudioKeepDays — через сколько дней удалять ЗВУК готовых записей. Текст
	// строки остаётся. Ноль — не удалять по возрасту. Умолчание — defaultAudioKeepDays:
	// файл без этого поля читается с ним (см. NewApp и readConfig), иначе ноль
	// молча означал бы «никогда».
	AudioKeepDays int `json:"audio_keep_days"`
	// AudioKeepDaysSet — срок выбрал сам человек (или нажал «Очистить сейчас»).
	// Тогда автоочистка не ждёт взведения уборки: человек ждёт результата. Без
	// этого флага срок остаётся умолчанием, и обновление не стирает звук в день
	// установки.
	AudioKeepDaysSet bool `json:"audio_keep_days_set,omitempty"`
	// PauseMedia — ставить ли на паузу играющее медиа на время диктовки. По
	// умолчанию да; в файле без этого поля тоже да (см. NewApp).
	PauseMedia bool `json:"pause_media"`
	// StopWaitSeconds — сколько ждать конца записи, когда диктовку просят
	// остановиться (установщик, скрипт выкатки, служба). Ноль — предел по
	// умолчанию, десять минут: файл, написанный до появления этого поля, не
	// должен менять поведение (см. stopWaitDuration).
	StopWaitSeconds int `json:"stop_wait_seconds,omitempty"`
	// ReleasedIME — системные сочетания переключения ввода, которые Диктовка
	// освободила, чтобы забрать Ctrl+Space. Хранится, чтобы вернуть их при
	// удалении программы и по кнопке «Вернуть системное сочетание».
	ReleasedIME []hotkey.IMEHotkey `json:"released_ime,omitempty"`
}

// HotkeyStatus — ответ окну на вопрос «свободно ли сочетание и кто его держит».
//
// Состояния те же, что у пакета hotkey, плюс ime-released: системное сочетание
// уже освобождено, но в нынешнем сеансе ctfmon ещё держит клавишу — она
// заработает после выхода и входа в Windows.
type HotkeyStatus struct {
	Hotkey  string `json:"hotkey"`
	State   string `json:"state"`
	Owner   string `json:"owner,omitempty"`
	Note    string `json:"note,omitempty"`
	IMEID   uint32 `json:"ime_id,omitempty"`
	IMEName string `json:"ime_name,omitempty"`
	// CanRelease — занявшее сочетание можно освободить штатно (это IME).
	CanRelease bool `json:"can_release,omitempty"`
	// Active — сочетание сейчас слушается Диктовкой.
	Active bool `json:"active,omitempty"`
}

// HotkeyStateIMEReleased — освобождено, но применится после входа в Windows.
const HotkeyStateIMEReleased = "ime-released"

// Entry — одна запись истории.
//
// ПОЛЯ ДОБАВЛЯЛИСЬ, А НЕ ЗАМЕНЯЛИСЬ. Старые history.json читаются как есть:
// пустой id заполняется датой, пустой status — словом done (normalizeHistory).
// Иначе история, записанная прежней версией, показалась бы окну сломанной.
type Entry struct {
	// ID — устойчивое имя записи: имя файла сессии без расширения
	// (20261111-193914). По нему окно просит удалить или расшифровать заново.
	ID          string    `json:"id"`
	At          time.Time `json:"at"`
	Text        string    `json:"text"`
	Seconds     float64   `json:"seconds"`
	Destination string    `json:"destination"`
	Audio       string    `json:"audio,omitempty"`
	// Status — done, error, transcribing, retrying или autostop. done — обычная
	// запись; retrying — ждёт автоповтора (его время в NextRetryAt); transcribing —
	// разбор идёт прямо сейчас; error — не вышло, причина в Error; autostop —
	// запись остановлена сама после пяти минут без голоса: без текста это не
	// ошибка, причина в Error только поясняет остановку. Поля с omitempty:
	// старые history.json обходятся без них, а у обычной удачной записи лишнего
	// мусора нет.
	Status string `json:"status,omitempty"`
	// Attempts — сколько автопопыток уже израсходовано.
	Attempts int `json:"attempts,omitempty"`
	// NextRetryAt — когда пробовать снова. Пусто — не запланировано.
	NextRetryAt time.Time `json:"next_retry_at,omitempty"`
	// Error — почему не вышло, коротко и по-русски: строка видна человеку.
	Error string `json:"error,omitempty"`
	// Recovered — запись восстановлена после сбоя, а не кончилась как обычно.
	Recovered bool `json:"recovered"`
}
type State struct {
	Transcript  string  `json:"transcript"`
	Destination string  `json:"destination"`
	Dark        bool    `json:"dark"`
	Version     string  `json:"version"`
	Stage       string  `json:"stage"`
	Seconds     float64 `json:"seconds"`
	Level       float64 `json:"level"`
	Trouble     string  `json:"trouble"`
	// DictationReady — диктовать можно: облачный способ собран и готов.
	DictationReady bool `json:"dictation_ready"`
	// Speech и SpeechNote — чем разбирается диктовка, имя способа и слова для
	// человека. Окну их не выдумать: прежде страница диктовки писала «GigaAM v3 ·
	// локально» жёстко, и надпись перестала быть правдой, когда разбор ушёл по сети.
	Speech     string `json:"speech"`
	SpeechNote string `json:"speech_note"`
	Microphone string `json:"microphone"`
	Config     Config `json:"config"`
	// Settings — те же настройки диктовки, что отдаёт GET /settings. Окно,
	// которое читает только /state, не должно остаться без способа разбора:
	// второго запроса ему делать незачем.
	Settings SettingsView `json:"settings"`
	// HistoryRev растёт при любой правке истории. Окно и плагин по нему видят,
	// что список изменился, не забирая сам список на каждом кадре.
	HistoryRev int64 `json:"history_rev"`
	// Pending — сколько строк ждёт повтора (unsent и retrying).
	Pending int `json:"pending"`
	// Provider — куда уходит звук и чем он подписан: вид способа, подпись для
	// человека, модель и узел. Окно не выдумывает подписи само — правило «одно
	// ядро» — и по kind выбирает, что показать рядом с моделью.
	Provider ProviderState `json:"provider"`
	// Last — последняя строка истории в лёгком кадре. Окну она нужна, чтобы
	// показать «последнюю заметку», не забирая весь список; в обычном кадре
	// поле не отдаётся: там есть history.
	Last *Entry `json:"-"`
	// History отсутствует в лёгком кадре (?lite=1): окну он нужен отдельным
	// запросом /history, а гонять 250 КБ каждые 150 мс незачем. В обычном кадре
	// поле есть всегда, даже пустое, — старые окна читают его как прежде.
	History         []Entry `json:"-"`
	HotkeyCapturing bool    `json:"hotkey_capturing"`
	// HotkeyNote — словами для человека, как взято сочетание: пусто — обычной
	// регистрацией; иначе «взято у системного переключения ввода» или
	// «освобождено, заработает после входа».
	HotkeyNote string `json:"hotkey_note,omitempty"`
	// HotkeyReleased — есть системное сочетание, которое можно вернуть.
	HotkeyReleased bool   `json:"hotkey_released,omitempty"`
	Recording      string `json:"recording,omitempty"`
	// RecordingActive — идёт ли сессия диктовки: запись или разбор после «стоп».
	// По нему установщик, скрипт выкатки и служба решают, можно ли трогать
	// файлы программы. Поле Recording (путь к файлу) для этого не годится: оно
	// называет последнюю запись и остаётся заполненным после конца разговора.
	RecordingActive bool `json:"recording_active"`
	// RecordingSince — когда началась идущая сессия; пусто, когда её нет.
	RecordingSince string `json:"recording_since,omitempty"`
	// Update — состояние обновления в лёгком кадре: тонкая плашка истории.
	Update *updates.State `json:"update,omitempty"`
	// lite — служебный признак кадра: история в него не кладётся.
	lite bool
}

// MarshalJSON кладёт историю в обычный кадр всегда, а в лёгкий — никогда.
//
// Одним omitempty этого не сделать: пустой список тогда исчезал бы и из
// обычного кадра, а старые окна читают поле как массив. Поэтому поле истории
// собирается здесь, а не тегом.
func (s State) MarshalJSON() ([]byte, error) {
	type alias State
	if s.lite {
		// last остаётся в лёгком кадре даже пустым: окно ждёт поле, а null —
		// честный ответ «история пуста». Именно поэтому поле не с omitempty.
		return json.Marshal(struct {
			alias
			History *[]Entry `json:"history,omitempty"`
			Last    *Entry   `json:"last"`
		}{alias: alias(s), Last: s.Last})
	}
	return json.Marshal(struct {
		alias
		History []Entry `json:"history"`
	}{alias: alias(s), History: s.History})
}

// UnmarshalJSON читает кадр обратно. Поле истории помечено "-", чтобы
// MarshalJSON собирал его сам, и без этого разбор кадра терял бы список: окну,
// значку и проверкам история нужна и в ответе, и в прочитанном состоянии.
func (s *State) UnmarshalJSON(data []byte) error {
	type alias State
	plain := struct {
		*alias
		History []Entry `json:"history"`
	}{alias: (*alias)(s)}
	if err := json.Unmarshal(data, &plain); err != nil {
		return err
	}
	s.History = plain.History
	return nil
}

type Endpoint struct {
	PID   int    `json:"pid"`
	URL   string `json:"url"`
	Token string `json:"token"`
}
type App struct {
	heartbeat atomic.Int64
	mu        sync.Mutex
	logMu     sync.Mutex
	// onceSeen — то, о чём за запуск уже сказано. Ожидаемый путь вставки через
	// буфер не должен на каждой диктовке засорять журнал одинаковой строкой.
	onceSeen   map[string]bool
	Dir, Media string
	Bin        string
	// tempDir — где искать брошенные временные файлы уборке. Пусто — системная
	// временная папка; проверка подставляет свою, чтобы не трогать чужое.
	tempDir string
	// recordingsVolume — предел объёма папки записей. Ноль — обычный предел
	// (1 ГБ); проверка ставит маленький, чтобы не писать гигабайт на диск.
	recordingsVolume int64
	// recoveryDone — проход восстановления при старте закончился. Уборка
	// сирот до этого не идёт вовсе: она может удалить файл, который
	// восстановление вот-вот вернёт в историю. Под a.mu.
	recoveryDone bool
	// sweepArmedAt — когда уборка впервые увидела файлы на этой версии. Первый
	// запуск после обновления только считает: удалять можно не раньше чем через
	// sweepArmDelay после этой метки. Под a.mu.
	sweepArmedAt time.Time
	// orphanMinAge — срок, после которого файл без строки истории считается
	// сиротой. Ноль — обычные 30 дней; проверка ставит короче.
	orphanMinAge time.Duration
	// storageMu, storageCache, storageAt — кэш объёма записей для окна: папку
	// не пересчитываем на каждый кадр, см. recordingsStats в audio_keep.go.
	storageMu    sync.Mutex
	storageCache recordingsStat
	storageAt    time.Time
	state        State
	audio        Audio
	overlay      *Overlay
	// overlayMu охраняет указатель на плашку: её больше не создают при старте
	// (это окно, шрифты и поверхность — мегабайты, которые в простое не нужны),
	// а поднимают при первой записи. Читают указатель нить клавиши, нить повтора
	// и такт службы, поэтому у него свой замок: плашка не должна ждать записи
	// звука.
	overlayMu sync.Mutex
	// overlayFinalized — итог текущей фразы уже отдан плашке. До следующего
	// Begin она не покажет «recording»: опоздавший такт дал бы второй звук.
	overlayFinalized atomic.Bool
	// idleSince — когда кончилась последняя диктовка; ноль — идёт запись.
	// lastTrim — когда последний раз отдавали рабочий набор системе, memoryTuned
	// — стоит ли сейчас мягкий предел кучи (перед записью его снимают).
	// Все три — под a.mu.
	idleSince   time.Time
	lastTrim    time.Time
	memoryTuned bool
	media       *mediapause.Pauser
	began       time.Time
	// phraseChannel — облачный способ, с которым началась текущая фраза.
	// Смена языка в настройках не трогает начатую запись: новый язык вступит
	// со следующего Begin. Под a.mu.
	phraseChannel speech.Channel
	target        Target
	keys          chan KeyEvent
	keyCancel     context.CancelFunc
	keyDone       chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
	work          sync.WaitGroup
	busy          bool
	session       *recordingSession
	captureUntil  time.Time
	// textOverride replaces cloud recognition in acceptance checks that drive the
	// streaming loop without a microphone or a network.
	textOverride func(string) (string, error)
	// Разговоры, которые ведут чужие программы: у каждого свой счёт слов.
	// Живут они до остановки или до того, как за ними перестанут приходить.
	speechMu sync.Mutex
	speech   map[string]*speechSession
	// restoreDecoder — чем восстанавливать старые .m4a без оглавления. Пусто —
	// искать ffmpeg; проверки подставляют своё.
	restoreDecoder aacrecover.Decoder
	// transcribeOverride заменяет распознавание в проверках восстановления и
	// повторной расшифровки: модель на диске и сеть там не нужны.
	transcribeOverride func(ctx context.Context, audio string) (Recognition, error)
	// retryGate пропускает одну сетевую попытку зараз: автоповтор и ручной
	// /retry не должны разбирать одну фразу одновременно.
	retryGate sync.Mutex
	// retryWake будит нить повтора сразу, когда появилась новая строка unsent.
	retryWake chan struct{}
	// retryEvery — как часто проверять сроки повтора. Ноль — секунда; в
	// проверке ставится короче, чтобы не ждать.
	retryEvery time.Duration
	// retryPauses — паузы между автопопытками. nil — обычные 30 с, 2 мин, 10 мин,
	// 30 мин, 2 ч; в проверке подменяются короткими.
	retryPauses []time.Duration
	// retryProbeDelay — пауза, когда облако не отвечает на лёгкую проверку.
	// Такая попытка не тратится: ждём и спрашиваем снова.
	retryProbeDelay time.Duration
	// historyWriteMu охраняет запись history.json вне a.mu: копия снимается под
	// замком, а на диск уходит после, и старая версия не должна затереть новую.
	historyWriteMu sync.Mutex
	historyWritten int64
	// clearedAt — когда человек очистил историю целиком. Всё, что записано
	// раньше, восстановление не возвращает: очистка — это решение, а не потеря.
	// Под a.mu.
	clearedAt time.Time
	// trimmedPending — строки, вытесненные пределом истории, которым ещё не
	// поставлена метка trimmed в журнале. Метка требует диска, а вытеснение
	// идёт под a.mu, поэтому копим и ставим метки после записи истории.
	// Под trimMu: карту читает writeHistory, который бывает и под a.mu.
	trimMu         sync.Mutex
	trimmedPending map[string]string
	// cloudProbe — лёгкий вопрос «облако ответило?». nil — настоящий запрос
	// (cloudReachable); в проверке подменяется, чтобы не ходить в сеть.
	cloudProbe func(context.Context) bool
	// notify — как сказать человеку о готовом тексте. nil — уведомление
	// Windows; в проверке подменяется, чтобы не рисовать на экране.
	notify func(title, text string)
	// audioStart — чем начинается запись; nil — настоящий микрофон. В проверках
	// подменяется, чтобы не требовать устройства.
	audioStart func() error
	// clock — часы записи: по ним считается пауза без голоса (автостоп, idle.go).
	// nil — настоящие часы; в проверках подменяются, чтобы пять минут тишины не
	// ждать на самом деле.
	clock func() time.Time
	// update — обновление по протоколу update/1. nil до Serve(): сеть и
	// проверка манифеста начинаются вместе со службой, а не при создании.
	update *updates.Controller
	// voiceMu, voicePath, voiceStamp и voiceValue — кэш voice.json по отпечатку
	// файла. Snapshot читает настройки несколько раз за кадр, и каждый разбор
	// файла с диска был бы платой за то, что меняется раз в день.
	voiceMu    sync.Mutex
	voicePath  string
	voiceStamp settingsStamp
	voiceValue *prefs.Voice
}

func NewApp(dir, bin string) *App {
	ctx, cancel := context.WithCancel(context.Background())
	media := DataDir()
	if !samePath(dir, SettingsDir()) {
		// A fixture owns its own media folder: a test must never write into the
		// real profile, and the real model stays where it is.
		media = filepath.Join(dir, "media")
	}
	// Preferences moved out of the profile; bring earlier versions along once
	// and repair a header that a killed process left short.
	if samePath(dir, SettingsDir()) {
		migrateSettings(dir, media)
	}
	repairRecordings(media)
	restoreState(dir, bin)
	config := Config{Hotkey: defaultHotkey, IdleSeconds: 300, PauseMedia: true, AudioKeepDays: defaultAudioKeepDays}
	if data, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		// Поле, которого нет в файле, остаётся включённым: файл написан до того,
		// как появилась пауза медиа, и молча выключать её нельзя. То же с сроком
		// хранения звука: без поля в файле действует умолчание, а не «никогда».
		saved := Config{PauseMedia: true, AudioKeepDays: defaultAudioKeepDays}
		if json.Unmarshal(data, &saved) == nil {
			// Пустое поле и прежнее умолчание Ctrl+D молча становятся нынешним;
			// выбранное человеком сочетание остаётся как было. Перенос идёт до
			// проверки: пустое сочетание иначе считалось бы испорченным файлом и
			// унесло бы с собой остальные настройки.
			saved.Hotkey = prefs.MigrateHotkey(saved.Hotkey)
			if validateConfig(saved) == nil {
				config = saved
			}
		}
	}
	// Ноль в поле предела ожидания здесь остаётся нулём: файл настроек
	// сравнивается с прочитанным как есть (в проверках и в окне), а «сколько
	// ждать» читает stopWaitDuration — там ноль и значит умолчание.
	app := &App{Dir: dir, Media: media, Bin: bin, keys: make(chan KeyEvent, 16), ctx: ctx, cancel: cancel, retryWake: make(chan struct{}, 1), idleSince: time.Now()}
	// Проверочная папка не трогает плееры человека: паузу ей подставляют руками.
	if samePath(dir, SettingsDir()) {
		app.media = newMediaPauser(app.sessionLog)
	}
	app.state = State{Version: Version, Stage: "idle", Microphone: "unchecked", Config: config, History: []Entry{}}
	if data, err := os.ReadFile(filepath.Join(dir, "history.json")); err == nil {
		_ = json.Unmarshal(data, &app.state.History)
	}
	app.state.History = normalizeHistory(app.state.History)
	// Когда историю очищали — по этой отметке восстановление понимает, что
	// записано до неё, и не возвращает очищенное.
	if data, err := os.ReadFile(filepath.Join(dir, historyStateFile)); err == nil {
		var saved historyState
		if json.Unmarshal(data, &saved) == nil {
			app.clearedAt = saved.ClearedAt
			app.sweepArmedAt = saved.SweepArmedAt
		}
	}
	// Номер версии истории: окно видит по нему, что список изменился.
	app.state.HistoryRev = 1
	app.state.Pending = countPending(app.state.History)
	// A recording that never finished still has its words on disk. Offer them
	// before anything else, so an interrupted conversation is not lost. Сюда же
	// попадают журналы с finish и recovered: их разговор уже разбирали, но
	// строки в history.json может не быть, и без этого восстановления она не
	// вернулась бы никогда.
	app.recoverAtStart()
	// Нераспознанные фразы прежних запусков: файл есть, строки в истории нет.
	// Человек увидит их в истории и сможет разобрать ещё раз.
	app.adoptLeftPhrases()
	// Строки, ждавшие облака, переживают перезапуск: retrying становится
	// unsent, и очередь продолжается без человека.
	app.restoreUnsent()
	return app
}

// sessionLogOnce пишет событие один раз на класс за запуск службы.
//
// «Поле не найдено, текст пойдёт через буфер» — не отказ, а обычный путь
// вставки в терминале и в чужом окне. Повторять его на каждой диктовке значит
// топить в нём настоящие отказы; поэтому класс события пишется однажды.
func (a *App) sessionLogOnce(event, class, message string) {
	key := event + "|" + class
	a.logMu.Lock()
	if a.onceSeen == nil {
		a.onceSeen = map[string]bool{}
	}
	if a.onceSeen[key] {
		a.logMu.Unlock()
		return
	}
	a.onceSeen[key] = true
	a.logMu.Unlock()
	a.sessionLog(event, message)
}

func validateConfig(config Config) error {
	if _, err := hotkey.Parse(config.Hotkey); err != nil {
		return err
	}
	if config.IdleSeconds < 15 || config.IdleSeconds > 900 {
		return errors.New("idle timeout must be 15-900 seconds")
	}
	if !validAudioKeepDays(config.AudioKeepDays) {
		return errors.New("audio keep days must be 0, 1, 7, 14, 30 or 90")
	}
	// Ноль — «предел по умолчанию»: файл настроек, написанный до появления
	// этого поля, не должен считаться испорченным.
	if config.StopWaitSeconds != 0 &&
		(config.StopWaitSeconds < int(MinStopWait/time.Second) || config.StopWaitSeconds > int(MaxStopWait/time.Second)) {
		return errors.New("stop wait must be 30-3600 seconds")
	}
	return nil
}

// ensureOverlay поднимает плашку при первой записи.
//
// Раньше окно плашки создавалось при старте службы и висело в памяти в простое:
// зарегистрированный класс, шрифты, поверхность кадра и страницы DWM. Первое
// нажатие всё равно не ждёт окна — Set только оставляет плашке последнее
// состояние, — поэтому создание окна отложено до записи и задержки показа не
// добавляет (замер в описании MR).
func (a *App) ensureOverlay() *Overlay {
	a.overlayMu.Lock()
	defer a.overlayMu.Unlock()
	if a.overlay == nil {
		a.overlay = NewOverlay(a.ctx)
		a.overlay.SetLog(a.sessionLog)
	}
	return a.overlay
}

// setOverlay оставляет плашке состояние. nil — плашка ещё не создана, и тогда
// показывать нечего: первая запись её и создаст.
func (a *App) setOverlay(state OverlayState) {
	// Итог фразы уже показан: опоздавший такт с «recording» перезапустил бы
	// анимацию, и та же фраза дала бы вторую галочку и второй звук.
	if a.overlayFinalized.Load() && state.Stage == "recording" {
		return
	}
	a.overlayMu.Lock()
	overlay := a.overlay
	a.overlayMu.Unlock()
	if overlay != nil {
		overlay.Set(state)
	}
}

// setFinalOverlay отдаёт плашке единственный итог фразы. Первый вызов
// побеждает: обычно это сразу после вставки в streaming.go, до записи истории;
// остальные пути идут через completeSession. Повтор не должен звучать второй
// раз и перезапускать анимацию.
func (a *App) setFinalOverlay(stage string) {
	if !a.overlayFinalized.CompareAndSwap(false, true) {
		return
	}
	a.mu.Lock()
	seconds := a.state.Seconds
	a.mu.Unlock()
	a.setOverlay(OverlayState{Visible: true, Stage: stage, Seconds: seconds})
}

// currentOverlay — плашка, если она уже поднята.
func (a *App) currentOverlay() *Overlay {
	a.overlayMu.Lock()
	defer a.overlayMu.Unlock()
	return a.overlay
}

// overlayStopped — канал закрытия плашки. Без плашки канал nil: такой случай в
// select не срабатывает никогда, и ждать нечего.
func (a *App) overlayStopped() <-chan struct{} {
	overlay := a.currentOverlay()
	if overlay == nil {
		return nil
	}
	return overlay.Stop
}

// overlayWindow — дескриптор окна плашки для уведомления. Ноль — плашки нет.
func (a *App) overlayWindow() uintptr {
	overlay := a.currentOverlay()
	if overlay == nil {
		return 0
	}
	return overlay.window.Load()
}
func (a *App) Snapshot() State { return a.snapshot(false) }

// SnapshotLite — то же состояние без истории: окну она нужна отдельным
// запросом /history, а таскать 250 КБ в каждом кадре незачем.
func (a *App) SnapshotLite() State { return a.snapshot(true) }

// snapshot собирает состояние. Тяжёлое (готовность облака, настройки, тема,
// подписи) считается ВНЕ a.mu: окно не должно ждать диск и реестр, а замок
// держит ещё и запись диктовки.
func (a *App) snapshot(lite bool) State {
	a.mu.Lock()
	state := a.state
	state.HotkeyCapturing = !a.captureUntil.IsZero()
	state.HotkeyReleased = len(state.Config.ReleasedIME) > 0
	stage, began := state.Stage, a.began
	// Копия истории — под замком и только когда её просят: в лёгком кадре
	// списка нет вовсе.
	state.lite = lite
	if lite {
		// История в лёгкий кадр не едет, но её хвост — последняя строка — едет:
		// окну он нужен, чтобы показать «последнюю заметку», не забирая весь
		// список. Копия, а не указатель на элемент: кадр собирается вне замка.
		if len(state.History) > 0 {
			last := state.History[0]
			state.Last = &last
		}
		state.History = nil
	} else {
		state.History = append([]Entry{}, state.History...)
	}
	a.mu.Unlock()

	voice := a.voice()
	// Диктовать можно тогда, когда собран облачный способ: ключ на месте и
	// адрес непустой. Местного пути у диктовки больше нет.
	channel := a.networkChannelForDictation()
	state.DictationReady = channel != nil
	state.Speech, state.SpeechNote = dictationSpeechFor(voice, channel)
	// Откуда звук уходит и чем подписан запрос — словами ядра, а не окна.
	state.Provider = a.provider(voice)
	state.Dark = DarkTheme()
	hotkey := state.Config.Hotkey
	if hotkey == "" {
		hotkey = prefs.DefaultHotkey
	}
	state.Settings = ReadSettings(voice, hotkey, state.Microphone)
	if stage == "recording" {
		state.Seconds = time.Since(began).Seconds()
		state.Level = a.audio.Level()
	}
	// Идёт ли сессия, видно и в обычном кадре, а не только в /status: окну и
	// значку второй запрос ни к чему, а правило «сессия жива» должно быть одно
	// на всю программу (sessionActiveStage).
	if sessionActiveStage(stage) {
		state.RecordingActive = true
		if !began.IsZero() {
			state.RecordingSince = began.Format(time.RFC3339)
		}
	}
	// Состояние обновления — в лёгком кадре: тонкая плашка истории не должна
	// ради одной строки спрашивать службу отдельным запросом.
	if lite && a.update != nil {
		current := a.update.State()
		state.Update = &current
	}
	return state
}

// voice читает voice.json, но не чаще, чем файл меняется.
//
// Один кадр состояния спрашивает настройки несколько раз (способ разбора,
// подписи, страница настроек), а файл лежит на диске и меняется раз в день.
// Кэш держит отпечаток: время правки и размер. Отпечаток изменился — файл
// перечитывается, и правка человека вступает в силу сразу.
// settingsStamp — отпечаток файла настроек: время правки и размер. По нему
// видно, что файл изменили, и читать с разбором его на каждой диктовке не надо.
type settingsStamp struct {
	modified int64
	size     int64
	present  bool
}

// stampOf — отпечаток файла настроек. Файла нет — пустой отпечаток: удаление
// настройки тоже правка, и её надо заметить.
func stampOf(path string) settingsStamp {
	info, err := os.Stat(path)
	if err != nil {
		return settingsStamp{}
	}
	return settingsStamp{modified: info.ModTime().UnixNano(), size: info.Size(), present: true}
}

// providersFile keeps the providers and their protected keys. Настройки
// распознавания читает internal/speech оттуда же: файл один на обе половины.
const providersFile = "providers.json"

func (a *App) voice() *prefs.Voice {
	path := prefs.VoicePath()
	stamp := stampOf(path)
	a.voiceMu.Lock()
	defer a.voiceMu.Unlock()
	if a.voiceValue != nil && a.voicePath == path && a.voiceStamp == stamp {
		return a.voiceValue
	}
	a.voiceValue = prefs.LoadVoice(path)
	a.voicePath, a.voiceStamp = path, stamp
	return a.voiceValue
}

func (a *App) Start() error {
	a.heartbeat.Store(time.Now().UnixNano())
	if err := os.MkdirAll(a.Dir, 0700); err != nil {
		return err
	}
	// Строки переноса ключей — в этот же журнал, и никуда иначе: ядро speech
	// журнала не заводит, а человеку нужна строка о расхождении ключей (какой
	// из двух верный, решает он). Фразу про повторы на процесс решает само
	// ядро (speech.SetLog), а место и имя события — здесь.
	speech.SetLog(func(line string) { a.sessionLog("speech", line) })
	if err := os.MkdirAll(a.Media, 0700); err != nil {
		return err
	}
	a.sessionLog("priority", fmt.Sprintf("raised=%t", RaisePriority()))
	// Плашку и менеджер медиасессий здесь больше не поднимаем. Окно плашки —
	// это зарегистрированный класс, шрифты, поверхность и страницы DWM: в
	// простое всё это лежало в памяти виджета, который ничего не показывает.
	// Плашка создаётся на первой записи (ensureOverlay), а менеджер медиасессий
	// поднимается первой же паузой — она и раньше шла в фоне и запись не ждала.
	// Такт простоя: сборка мусора, мягкий предел кучи и обрезка рабочего набора
	// (см. memory.go).
	a.work.Add(1)
	go func() {
		defer a.work.Done()
		a.memoryLoop(a.ctx)
	}()
	if err := a.setKeys(a.state.Config); err != nil {
		a.noteHotkeyProblem(a.state.Config)
	}
	// Тяжёлая часть восстановления после сбоя — разбор записей без текста и
	// старых .m4a — в фоне: распознавание длится дольше, чем человек готов
	// ждать запуска. Текст, дошедший до журнала, уже показан в NewApp.
	//
	// За восстановлением идёт уборка: сперва служба возвращает то, что потеряла,
	// и только потом убирает сирот. Обратный порядок удалил бы запись, которую
	// восстановление как раз собиралось разобрать. Дальше уборка идёт раз в сутки.
	a.work.Add(1)
	go func() {
		defer a.work.Done()
		a.storageLoop(a.ctx)
	}()
	// Фразы, не дошедшие до облака, ждут в очереди: пока они есть, служба раз в
	// полминуты спрашивает облако и разбирает сохранённое, когда оно ответит.
	a.work.Add(1)
	go func() {
		defer a.work.Done()
		a.retryLoop(a.ctx)
	}()
	a.work.Add(1)
	go func() {
		defer a.work.Done()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case event := <-a.keys:
				a.keyPressed(event.Target)
			case <-a.overlayStopped():
				a.Finish(false)
			case <-ticker.C:
				a.tick()
			}
		}
	}()
	return nil
}

// keyPressed — что делать по нажатию сочетания.
//
// Режим один: не идёт запись — начинаем, идёт — заканчиваем. Вынесено из цикла
// событий, чтобы это правило проверялось без настоящей клавиатуры: проверка
// нажимает так же, как человек, а сочетание при этом не занимается.
func (a *App) keyPressed(target Target) {
	// Нажатие не должно стоить целого состояния: снимок читает файлы, реестр и
	// историю. Достаточно того, что под замком.
	a.mu.Lock()
	capturing, stage := !a.captureUntil.IsZero(), a.state.Stage
	a.mu.Unlock()
	if capturing {
		return
	}
	if stage == "recording" {
		a.Finish(false)
		return
	}
	a.Begin(target)
}

// tick refreshes the heartbeat, the overlay and idle unloading. A recording does
// not end by time while there is voice in it: a conversation may last a minute
// or three hours. The only time-based stop is the automatic one after five
// minutes without voice (idle.go); otherwise the user decides when it stops.
func (a *App) tick() {
	a.heartbeat.Store(time.Now().UnixNano())
	a.mu.Lock()
	if !a.captureUntil.IsZero() && time.Now().After(a.captureUntil) {
		a.resumeHotkeyLocked()
	}
	stage, began := a.state.Stage, a.began
	if stage == "recording" {
		a.setOverlay(OverlayState{true, stage, time.Since(began).Seconds(), a.audio.Level()})
	}
	a.mu.Unlock()
}
func (a *App) setKeys(config Config) error {
	combo, err := hotkey.Parse(config.Hotkey)
	if err != nil {
		return err
	}
	a.stopKeysLocked()
	ctx, cancel := context.WithCancel(a.ctx)
	done := make(chan struct{})
	ready := make(chan error, 1)
	go func() { defer close(done); ListenKeys(ctx, combo, a.keys, ready) }()
	if err = <-ready; err != nil {
		cancel()
		<-done
		a.keyCancel = nil
		return err
	}
	a.keyCancel, a.keyDone = cancel, done
	return nil
}
func (a *App) stopKeysLocked() {
	if a.keyCancel != nil {
		a.keyCancel()
		<-a.keyDone
		a.keyCancel = nil
	}
	// Discard presses queued before capture began or a shortcut changed.
	for {
		select {
		case <-a.keys:
		default:
			return
		}
	}
}
func (a *App) resumeHotkeyLocked() {
	a.captureUntil = time.Time{}
	if err := a.setKeys(a.state.Config); err != nil {
		a.state.Trouble = err.Error()
	}
}
func (a *App) CaptureHotkey(active bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return errors.New("finish recording before changing the shortcut")
	}
	if active {
		if a.captureUntil.IsZero() {
			a.stopKeysLocked()
		}
		// The UI renews this lease. Closing or crashing it cannot leave the
		// global shortcut suspended indefinitely.
		a.captureUntil = time.Now().Add(15 * time.Second)
	} else if !a.captureUntil.IsZero() {
		a.resumeHotkeyLocked()
	}
	return nil
}
func (a *App) Configure(config Config) error {
	if err := validateConfig(config); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return errors.New("finish recording before changing settings")
	}
	combo, _ := hotkey.Parse(config.Hotkey)
	config.Hotkey = combo.String()
	previous := a.state.Config
	// Смена срока человеком — его выбор: флаг сохраняется вместе с настройкой.
	// Повтор того же значения (его шлёт окно вместе с паузой музыки) флаг не
	// трогает, иначе умолчание стало бы выбором без единого нажатия.
	sameKeep := config.AudioKeepDays == previous.AudioKeepDays
	// Флаг выбора: смена срока ставит его, повтор того же значения берёт из
	// прежней настройки, а не из входящей (пустая просьба флаг не сбросит).
	if sameKeep {
		config.AudioKeepDaysSet = previous.AudioKeepDaysSet
	} else {
		config.AudioKeepDaysSet = true
	}
	if err := a.setKeys(config); err != nil {
		_ = a.setKeys(previous)
		return err
	}
	if err := a.writeState("config.json", config); err != nil {
		_ = a.setKeys(previous)
		return err
	}
	a.state.Config = config
	a.state.Trouble = ""
	a.state.HotkeyNote = ""
	if !sameKeep {
		// Внеочередная уборка — в фоне: Configure держит замок, а уборка берёт его
		// сама. kickAutoclean сам проверит, что восстановление уже прошло.
		a.work.Add(1)
		go func() {
			defer a.work.Done()
			a.kickAutoclean()
		}()
	}
	return nil
}

// startRefusal — почему диктовку начинать нельзя; пусто — можно. Зовётся под
// a.mu. Вынесено из Begin, чтобы решение проверялось без микрофона.
//
// Местного разбора больше нет (решение владельца): диктовка идёт только
// облаком, и неготовое облако — это отказ с причиной, а не повод взять модель
// на машине.
func startRefusal(cloudReady bool) string {
	if cloudReady {
		return ""
	}
	return "Облачное распознавание не готово: не вписан ключ или адрес сервиса. Проверьте способ в настройках."
}

// timeNow — текущее время для записи: настоящее или подменённое в проверке.
func (a *App) timeNow() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now()
}

func (a *App) Begin(target Target) {
	// Запись начинается — снимаем мягкий предел кучи и возвращаем обычный темп
	// сборки: разговор в час занимает в памяти сто мегабайт звука, и предел
	// простоя заставил бы сборщик работать на каждой фразе.
	a.releaseMemoryPressure()
	// Облачный способ собирается до замка: вопрос к настройкам может писать в
	// журнал, а журнал берёт свой замок.
	cloud := a.networkChannel()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy || !a.captureUntil.IsZero() {
		return
	}
	if trouble := startRefusal(cloud != nil); trouble != "" {
		a.state.Trouble = trouble
		a.state.Stage = "error"
		return
	}
	// Способ фразы фиксируется в её начале: язык и ключ, изменённые в настройках
	// посреди записи, не должны переделать уже идущую фразу.
	a.phraseChannel = cloud
	// Плашка создаётся здесь, на первой записи, а не при старте службы.
	a.ensureOverlay().Set(OverlayState{Visible: true, Stage: "recording"})
	startAudio := a.audio.Start
	if a.audioStart != nil {
		startAudio = a.audioStart
	}
	if err := startAudio(); err != nil {
		a.state.Trouble = "Микрофон недоступен. Проверьте устройство и разрешения Windows."
		a.state.Microphone = "error"
		a.state.Stage = "error"
		a.setOverlay(OverlayState{})
		return
	}
	a.busy = true
	a.idleSince = time.Time{}
	a.target = target
	a.began = time.Now()
	// Запись уже идёт: пауза чужого медиа делается в фоне и старт не задерживает.
	a.pauseMedia()
	// Точка отсчёта: дальше все отметки timing считаются от неё, и по ним видно
	// и задержку до начала записи, и всё, что происходит после разбора.
	a.sessionLog("timing", "begin ms=0")
	a.overlayFinalized.Store(false)
	a.state.Stage = "recording"
	a.state.Trouble = ""
	a.state.Microphone = "ready"
	a.state.Transcript = ""
	a.state.Destination = ""
	// Запись прошлого разговора не должна достаться новому: если файл не
	// откроется, журнал назовёт чужой звук.
	a.state.Recording = ""
	a.session = &recordingSession{stop: make(chan struct{}), started: a.timeNow()}
	// The session audio is kept: a failed recognition, a broken session or a
	// second model can always be replayed against the same recording. Пишется
	// Ogg/Opus: сжатие идёт по ходу, страница сбрасывается на диск раз в
	// секунду, и оборванный файл читается до последней целой страницы
	// (audio.go, internal/opusrec).
	path := filepath.Join(a.Media, "recordings", time.Now().Format("20060102-150405")+".ogg")
	if err := a.audio.Record(path); err != nil {
		a.sessionLog("recording-file-unavailable", err.Error())
	} else {
		a.state.Recording = path
		if real := a.audio.RecordedPath(); real != "" {
			a.state.Recording = real
		}
	}
	// Words are written to disk as they are recognized: a lost process must not
	// take the conversation with it. Путь к записи едет в первом же событии:
	// восстановлению нужно знать, что расшифровывать после вылета.
	if record, err := openJournal(a.Dir, a.state.Recording); err == nil {
		a.session.journal = record
	} else {
		a.sessionLog("journal-unavailable", err.Error())
	}
	a.setOverlay(OverlayState{Visible: true, Stage: "recording"})
	a.work.Add(1)
	go a.streamRecording(a.session, target)
}

// pauseMedia ставит играющее медиа на паузу, если это не выключено в настройках.
// Зовётся под a.mu: внутри только постановка работы в фон.
func (a *App) pauseMedia() {
	if a.state.Config.PauseMedia {
		a.media.Pause()
	}
}
func (a *App) Finish(cancelled bool) {
	a.mu.Lock()
	if a.state.Stage != "recording" {
		a.mu.Unlock()
		return
	}
	// Отпускание клавиши: от этой отметки до готовности последнего куска видно,
	// сколько звуковой слой отдаёт хвост записи. Раньше этот отрезок не мерился
	// вовсе, а именно в нём могла прятаться часть ожидания.
	a.sessionLog("timing", fmt.Sprintf("release ms=%d", time.Since(a.began).Milliseconds()))
	_ = a.audio.Stop()
	seconds := time.Since(a.began).Seconds()
	// Плашка не гаснет на отпускании: текст ещё готовится, и человек видит,
	// что ждать есть чего. Гаснет она на итоге — галочкой или надписью.
	a.setOverlay(OverlayState{Visible: true, Stage: "processing", Seconds: seconds})
	a.state.Seconds = seconds
	a.state.Stage = "processing"
	a.session.cancelled.Store(cancelled || seconds < .25)
	// Остановка приходит и от человека, и от выключения Windows, и обе могут
	// совпасть: канал закрывается ровно один раз.
	a.session.end()
	a.mu.Unlock()
}

// stageKeepsHistory — у разговора с таким итогом строка в истории обязана быть.
// Тишина и отмена её не заводят, прерванный выключением разговор разберут при
// следующем запуске, а всё остальное — включая отказ распознавания — должно
// попасть в history.json ДО метки в журнале.
func stageKeepsHistory(stage string) bool {
	return stage != "silence" && stage != "cancelled" && stage != "interrupted"
}

func (a *App) complete(stage, text string, err error) {
	a.completeWithSession(nil, stage, text, err)
}

// completeWithSession завершает фразу и отпускает её облачный канал. Канал
// живёт ровно до итога: иначе смена языка/ключа после первой диктовки не
// действует на повтор, /history/retranscribe, API TranscribeFile и готовность
// службы — все они читают networkChannelForDictation.
//
// Сессия передаётся, чтобы опоздавший итог старой фразы не стёр канал новой,
// которая уже началась. Прямые вызовы complete без сессии (проверки) очищают
// канал только когда записи нет.
func (a *App) completeWithSession(session *recordingSession, stage, text string, err error) {
	a.mu.Lock()
	// Опоздавший итог фразы, пришедший после Begin новой, не меняет ничего: ни канал,
	// ни занятость, ни стадию. Иначе пятая по счёту нить вывела бы службу в
	// простой посреди живой записи.
	if session != nil && a.session != session {
		a.mu.Unlock()
		return
	}
	// В боевом пути итог уже отдан плашке completeSession до записи истории.
	// Прямые вызовы complete не должны ждать закрытия записи и уборки: «готово»
	// уходит первым делом, ещё до CloseRecording. Один итог на фразу
	// гарантирует отметка completeSession — здесь повтора не будет.
	if !a.overlayFinalized.Load() {
		a.setOverlay(OverlayState{Visible: true, Stage: stage, Seconds: a.state.Seconds})
	}
	a.sessionLog("timing", fmt.Sprintf("overlay-set stage=%s ms=%d", stage, time.Since(a.began).Milliseconds()))
	// Диктовка кончилась (вставили, отменили, ошибка): плееры, которые остановили
	// мы, продолжают. Работа идёт в фоне.
	a.media.Resume()
	if a.session != nil && a.session.journal != nil {
		switch {
		case stage == "interrupted":
			// Выключение машины: журнал закрывается БЕЗ события finish. Так
			// следующий запуск видит незаконченный разговор и разбирает его
			// сам: слова и звук уже на диске, а «готово» было бы неправдой.
			a.session.journal.close()
		case stageKeepsHistory(stage) && !a.session.historySaved:
			// Итог не лёг в history.json: метку в журнале не ставим, иначе
			// восстановление сочло бы разговор разобранным и потеряло бы его.
			// Журнал остаётся без finish, и следующий запуск заведёт запись.
			a.session.journal.close()
			a.sessionLog("journal-kept", "stage="+stage+" reason=history-not-saved")
		default:
			a.session.journal.finish(stage, text, journalStatus(stage, err, text))
		}
		a.session.journal = nil
	}
	pruneDir := ""
	if path := a.audio.CloseRecording(); path != "" {
		// Автостоп без текста: слов нет, звук не нужен — как при тишине. Строка
		// истории уже заведена без пути к звуку (см. finishAutostopEmpty).
		if stage == "silence" || stage == "cancelled" || (stage == "autostop" && text == "") {
			_ = os.Remove(path)
			a.state.Recording = ""
		} else {
			// Настоящее имя — то, что вернула запись: у сжатой записи оно
			// другое, и без этого сжатие после остановки пыталось бы упаковать
			// несуществующий .wav.
			a.state.Recording = path
			// Чистка идёт после замка и по ОДНОМУ пределу на все расширения:
			// три отдельных предела давали втрое больше файлов, чем задумано.
			pruneDir = filepath.Dir(path)
		}
	}
	// Фраза кончилась: её язык и ключ больше никого не связывают. Опоздавший
	// итог другой сессии не трогает канал уже идущей фразы.
	switch {
	case session == nil:
		if !a.busy {
			a.phraseChannel = nil
		}
	case a.session == session:
		a.phraseChannel = nil
	}
	a.busy = false
	// Отсюда и до следующей записи — простой; такт памяти по нему решает, когда
	// отдавать рабочий набор системе (memory.go).
	a.idleSince = time.Now()
	a.state.Stage = stage
	if err != nil {
		a.state.Trouble = err.Error()
	} else {
		a.state.Trouble = ""
	}
	a.sessionLog("timing", fmt.Sprintf("complete-done ms=%d", time.Since(a.began).Milliseconds()))
	a.mu.Unlock()
	if pruneDir != "" {
		pruneRecordings(pruneDir, recordingLimit)
	}
	// Память возвращается системе после каждой диктовки, а не когда-нибудь:
	// распознаватель и звук оставляют в куче сотни мегабайт, и на машине с 8 ГБ
	// это видно как «диктовка съела память». Вызов идёт в фоне и без a.mu:
	// сборка мусора не должна задерживать окно и следующее нажатие.
	go freeOSMemory()
}
func (a *App) CheckMic() error {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return errors.New("recording is busy")
	}
	a.busy = true
	a.state.Microphone = "checking"
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.busy = false; a.mu.Unlock() }()
	var test Audio
	err := test.Start()
	var data []byte
	if err == nil {
		select {
		case <-time.After(400 * time.Millisecond):
		case <-a.ctx.Done():
		}
		data = test.Stop()
		if len(data) == 0 {
			err = errors.New("no microphone samples received")
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.state.Microphone = "error"
		a.state.Trouble = "Микрофон недоступен. Откройте разрешения Windows и включите доступ для классических приложений."
		return err
	}
	a.state.Microphone = "ready"
	a.state.Trouble = ""
	return nil
}
func (a *App) Close() {
	speech.SetLog(nil)
	a.Finish(true)
	a.cancel()
	if a.keyCancel != nil {
		a.keyCancel()
		<-a.keyDone
	}
	a.work.Wait()
	// Выход посреди диктовки не оставляет музыку на паузе.
	a.media.Resume()
	a.media.Close()
	if overlay := a.currentOverlay(); overlay != nil {
		overlay.Wait()
	}
}
func (a *App) OpenUI() {
	if ShowExistingUI() {
		return
	}
	command := exec.Command(filepath.Join(a.Bin, "dictation-ui.exe"), "--dictation")
	_ = command.Start()
	go func() { _ = command.Wait() }()
}

// writeJSON пишет файл целиком и подменяет им прежний: временный файл, сброс на
// диск, переименование. Sync здесь не для красоты — история пишется ПЕРВОЙ, а
// метка в журнале второй, и обещание «запись уже на диске» должно пережить
// сбой питания, а не только закрытие файла.
func writeJSON(path string, value any) error {
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(JSON(value)); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// recognizePhrase разбирает одну фразу дороги диктовки.
//
// РАЗБОР ТОЛЬКО ОБЛАЧНЫЙ: местного пути у диктовки больше нет (решение
// владельца). Отказ облака — это ошибка с причиной, которую видно в окне, а звук
// остаётся на диске и встаёт в очередь повтора (см. retry.go).
//
// Повтор фразы живёт не здесь, а в очереди истории (retry.go): один отказ не
// должен держать поле пустым лишние секунды, а паузы 30 с, 2 мин и дальше
// отсчитываются уже после того, как человек отпустил клавишу.
func (a *App) recognizePhrase(ctx context.Context, path string) (string, error) {
	return a.recognizePhraseWithPrompt(ctx, path, "")
}

// recognizePhraseWithPrompt — то же распознавание, но с хвостом предыдущего
// текста в подсказке: так куски диктовки склеиваются связнее.
func (a *App) recognizePhraseWithPrompt(ctx context.Context, path, prompt string) (string, error) {
	if a.textOverride != nil {
		return a.textOverride(path)
	}
	result, err, attempted := a.networkAttemptWithPrompt(ctx, path, prompt)
	if attempted && err == nil {
		return result.Text, nil
	}
	// Причина — прямо из отказа, а не из итога «облака»: навык подписи (ключ,
	// 429, сеть) есть только у отказа, и подпись должна остаться той же, что у
	// проверки ключа.
	if attempted {
		return "", cloudRefusal(err)
	}
	// Идти некуда: не вписан ключ, адрес или способ.
	return "", cloudRefusal(nil)
}

func (a *App) Serve() (*http.Server, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		_ = listener.Close()
		return nil, err
	}
	endpoint := Endpoint{PID: os.Getpid(), URL: "http://" + listener.Addr().String(), Token: hex.EncodeToString(secret)}
	if err = os.MkdirAll(a.Media, 0700); err != nil {
		_ = listener.Close()
		return nil, err
	}
	if err = writeJSON(filepath.Join(a.Media, "endpoint.json"), endpoint); err != nil {
		_ = listener.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	writeState := func(w http.ResponseWriter, state State) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	}
	respond := func(w http.ResponseWriter) { writeState(w, a.Snapshot()) }
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("lite") == "1" {
			writeState(w, a.SnapshotLite())
			return
		}
		// Без lite — как раньше: старые окна и значок не должны остаться без
		// истории из-за новой ручки.
		respond(w)
	})
	// История отдельной ручкой: окно берёт её по номеру версии, а не таскает в
	// каждом кадре состояния. Ответ называет версию, по которой видно, что
	// список изменился.
	mux.HandleFunc("GET /history", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		rev := a.state.HistoryRev
		history := append([]Entry{}, a.state.History...)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"rev": rev, "history": history})
	})
	a.settingsRoutes(mux)
	a.providerRoutes(mux)
	a.retryRoute(mux, respond)
	a.historyActionsRoutes(mux)
	a.audioKeepRoutes(mux)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if time.Since(time.Unix(0, a.heartbeat.Load())) > 3*time.Second {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		// ?lite=1 — без истории: окно забирает её отдельно по /history.
		lite := r.URL.Query().Get("lite") == "1"
		previous := ""
		for {
			state := a.snapshot(lite)
			value := string(JSON(state))
			// Замок уже отпущен: снимок сделан выше, и запись в сокет не держит
			// ни историю, ни настройки.
			if value != previous {
				if _, err := fmt.Fprintf(w, "data: %s\n\n", value); err != nil {
					return
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					return
				}
				previous = value
			}
			// Такт частый только пока идёт запись или разбор: в простое нет
			// смысла будить окно шесть раз в секунду.
			wait := time.Second
			if state.Stage == "recording" || state.Stage == "processing" {
				wait = 150 * time.Millisecond
			}
			timer := time.NewTimer(wait)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return
			case <-a.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	})
	mux.HandleFunc("POST /toggle", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		stage := a.state.Stage
		a.mu.Unlock()
		if stage == "recording" {
			a.Finish(false)
		} else {
			a.Begin(Target{})
		}
		respond(w)
	})
	mux.HandleFunc("POST /config", func(w http.ResponseWriter, r *http.Request) {
		// Основа — нынешние настройки: окно прежней версии не знает про
		// pause_media и не должно молча менять его.
		a.mu.Lock()
		config := a.state.Config
		a.mu.Unlock()
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := a.Configure(config); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		respond(w)
	})
	mux.HandleFunc("POST /hotkey-capture", func(w http.ResponseWriter, r *http.Request) {
		var data struct {
			Active bool `json:"active"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			http.Error(w, "invalid capture request", http.StatusBadRequest)
			return
		}
		if err := a.CaptureHotkey(data.Active); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		respond(w)
	})
	mux.HandleFunc("POST /hotkey-check", func(w http.ResponseWriter, r *http.Request) {
		var data struct {
			Hotkey string `json:"hotkey"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			http.Error(w, "invalid hotkey request", http.StatusBadRequest)
			return
		}
		status, err := a.CheckHotkey(data.Hotkey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})
	mux.HandleFunc("POST /hotkey-ime/release", func(w http.ResponseWriter, r *http.Request) {
		var data struct {
			Hotkey string `json:"hotkey"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			http.Error(w, "invalid hotkey request", http.StatusBadRequest)
			return
		}
		status, err := a.ReleaseHotkey(data.Hotkey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})
	mux.HandleFunc("POST /hotkey-ime/restore", func(w http.ResponseWriter, r *http.Request) {
		restored, err := a.RestoreHotkey()
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		a.mu.Lock()
		note := a.state.Trouble
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"restored": restored, "note": note})
	})
	// Чужая программа — например, окно агентской оболочки — присылает запись и
	// получает слова. Тело запроса и есть звук, а не путь к нему: у того, кто
	// присылает, нет доступа к нашим папкам, и путь был бы просьбой прочитать
	// файл, которого он не видит. Запись живёт до конца разбора и стирается.
	mux.HandleFunc("POST /transcribe", func(w http.ResponseWriter, r *http.Request) {
		// Разбор записи длиной в минуты не укладывается в общий срок ответа.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTranscribeBytes))
		if err != nil {
			http.Error(w, "запись не прочитана", http.StatusBadRequest)
			return
		}
		extension, known := recordingFormat(body)
		if !known {
			http.Error(w, "это не запись: не узнаю ни WAV, ни Opus в контейнере WebM", http.StatusBadRequest)
			return
		}
		file, err := os.CreateTemp("", "dictation-outside-*"+extension)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		name := file.Name()
		defer func() { _ = os.Remove(name) }()
		if _, err = file.Write(body); err != nil {
			_ = file.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err = file.Close(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		result, err := a.TranscribeFile(r.Context(), name)
		if err != nil {
			status := http.StatusConflict
			if errors.Is(err, errAudioNotFound) {
				status = http.StatusBadRequest
			}
			http.Error(w, err.Error(), status)
			return
		}
		text := result.Text
		// Запись остаётся у нас, а не только в ответе: чужое окно её не хранит,
		// и без этого послушать сказанное было бы нечем.
		if kept := a.keepRecording(body); kept != "" {
			// В историю идёт длина разговора, а не время разбора: человек ищет
			// запись по тому, сколько он говорил, и минута разбора на десяти
			// минутах речи показывала бы не то, что было.
			a.rememberPlugged(text, kept, audioSecondsOf(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"text":    text,
			"model":   result.ID,
			"name":    result.Name,
			"seconds": result.Seconds,
			"error":   result.Error,
		})
	})
	// Разговор, а не одна запись: чужая программа присылает звук кусками и
	// забирает слова по мере разбора. Так диктовка попадает в поле по мере речи,
	// а не одним куском в конце, — но разбирает её наш распознаватель, а не чужой.
	mux.HandleFunc("POST /speech/start", func(w http.ResponseWriter, r *http.Request) {
		id, channel, err := a.SpeechStart()
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          id,
			"model":       channel.Name(),
			"name":        channel.Title(),
			"sample_rate": speechRate,
			"error":       "",
		})
	})
	mux.HandleFunc("POST /speech/feed", func(w http.ResponseWriter, r *http.Request) {
		// Разбор куска идёт дольше общего срока ответа: тёплый распознаватель
		// успевает быстро, но фраза длиной в минуты и его задерживает.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		var data struct {
			ID    string `json:"id"`
			PCM   string `json:"pcm"`
			Pause bool   `json:"pause"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxSpeechBytes)
		if json.NewDecoder(r.Body).Decode(&data) != nil {
			http.Error(w, "просьба не разобрана", http.StatusBadRequest)
			return
		}
		pcm, err := base64.StdEncoding.DecodeString(data.PCM)
		if err != nil {
			http.Error(w, "звук не разобран", http.StatusBadRequest)
			return
		}
		text, err := a.SpeechFeed(r.Context(), data.ID, pcm, data.Pause)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": text, "error": ""})
	})
	mux.HandleFunc("POST /speech/stop", func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		var data struct {
			ID string `json:"id"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if json.NewDecoder(r.Body).Decode(&data) != nil {
			http.Error(w, "просьба не разобрана", http.StatusBadRequest)
			return
		}
		text, seconds, err := a.SpeechStop(r.Context(), data.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": text, "seconds": seconds, "error": ""})
	})
	mux.HandleFunc("POST /speech/key", func(w http.ResponseWriter, r *http.Request) {
		var data struct {
			Provider string `json:"provider"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if json.NewDecoder(r.Body).Decode(&data) != nil {
			http.Error(w, "просьба не разобрана", http.StatusBadRequest)
			return
		}
		// Ключ отдаётся только тому, кто прочитал сведения о службе: адрес и
		// токен лежат в файле, доступном учётной записи человека, а запросы из
		// браузера отвергает общая проверка выше. Нужен он затем, чтобы чужая
		// программа могла сходить в облако сама — без нас и без ожидания.
		found, why := a.cloudKey(data.Provider)
		w.Header().Set("Content-Type", "application/json")
		if why != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": why})
			return
		}
		_ = json.NewEncoder(w).Encode(found)
	})
	mux.HandleFunc("POST /microphone", func(w http.ResponseWriter, r *http.Request) {
		if err := a.CheckMic(); err != nil {
			http.Error(w, "microphone is unavailable", http.StatusConflict)
			return
		}
		respond(w)
	})
	mux.HandleFunc("POST /permissions", func(w http.ResponseWriter, r *http.Request) { OpenMicrophoneSettings(); respond(w) })
	mux.HandleFunc("POST /cancel", func(w http.ResponseWriter, r *http.Request) { a.Finish(true); respond(w) })
	mux.HandleFunc("POST /copy", func(w http.ResponseWriter, r *http.Request) {
		var data struct {
			Text string `json:"text"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		if json.NewDecoder(r.Body).Decode(&data) != nil {
			http.Error(w, "invalid text", 400)
			return
		}
		if err := Clipboard(data.Text); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		respond(w)
	})
	mux.HandleFunc("POST /clear-history", func(w http.ResponseWriter, r *http.Request) {
		clearedAt := time.Now()
		// Отметки — ПЕРВЫМИ, история — после. Иначе сбой между двумя записями
		// вернул бы очищенную историю целиком при следующем запуске.
		a.mu.Lock()
		a.clearedAt = clearedAt
		armed := a.sweepArmedAt
		a.mu.Unlock()
		if err := a.writeState(historyStateFile, historyState{ClearedAt: clearedAt, SweepArmedAt: armed}); err != nil {
			a.sessionLog("history-state-write-failed", err.Error())
		}
		if count := a.markJournalsCleared(); count > 0 {
			a.sessionLog("history-cleared", fmt.Sprintf("journals=%d", count))
		}
		a.mu.Lock()
		a.state.History = []Entry{}
		a.touchHistoryLocked()
		a.mu.Unlock()
		a.saveHistory()
		respond(w)
	})
	mux.HandleFunc("POST /history/delete", func(w http.ResponseWriter, r *http.Request) {
		var data struct {
			ID string `json:"id"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if json.NewDecoder(r.Body).Decode(&data) != nil || strings.TrimSpace(data.ID) == "" {
			http.Error(w, "не названа запись", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		index, found := a.findHistoryLocked(data.ID)
		if !found {
			a.mu.Unlock()
			http.Error(w, "запись не найдена", http.StatusNotFound)
			return
		}
		entry := a.state.History[index]
		audio := entry.Audio
		// Метка в журнале — ПЕРВОЙ. Удаление убирает звук, но текст журнала
		// пережил бы его, и без метки восстановление вернуло бы запись при
		// следующем запуске. Сбой между меткой и историей уберёт
		// dropDiscardedHistory при старте; обратный порядок воскрешал бы запись.
		a.markJournalDeleted(entry.ID, audio)
		a.state.History = append(a.state.History[:index], a.state.History[index+1:]...)
		// Один и тот же звук может стоять у двух записей (например, у разговора
		// и его повтора) — тогда файл остаётся, пока на него кто-то ссылается.
		shared := false
		for _, entry := range a.state.History {
			if entry.Audio != "" && samePath(entry.Audio, audio) {
				shared = true
				break
			}
		}
		err := a.saveHistoryLocked()
		a.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !shared && a.historyFileAllowed(audio) {
			_ = os.Remove(audio)
		}
		respond(w)
	})
	mux.HandleFunc("POST /history/retranscribe", func(w http.ResponseWriter, r *http.Request) {
		var data struct {
			ID string `json:"id"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if json.NewDecoder(r.Body).Decode(&data) != nil || strings.TrimSpace(data.ID) == "" {
			http.Error(w, "не названа запись", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		index, found := a.findHistoryLocked(data.ID)
		if !found {
			a.mu.Unlock()
			http.Error(w, "запись не найдена", http.StatusNotFound)
			return
		}
		entry := a.state.History[index]
		if entry.Audio == "" {
			a.mu.Unlock()
			http.Error(w, "звук не сохранился", http.StatusConflict)
			return
		}
		if info, err := os.Stat(entry.Audio); err != nil || info.IsDir() || !a.historyFileAllowed(entry.Audio) {
			a.mu.Unlock()
			http.Error(w, "звук не сохранился", http.StatusConflict)
			return
		}
		a.state.History[index].Status = statusTranscribing
		a.state.History[index].Error = ""
		err := a.saveHistoryLocked()
		a.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Разбор идёт в фоне: ответ возвращается сразу, а окно видит
		// transcribing и потом спрашивает состояние, пока статус не сменится.
		go a.retranscribe(entry.ID, entry.Audio)
		respond(w)
	})
	// Остановка: /status говорит, идёт ли запись, /quit ждёт её конца. Вынесено
	// в stop.go — по этому ответу установщик и скрипт выкатки решают, можно ли
	// менять файлы.
	a.stopRoutes(mux)
	// Обновление: своя ручка и фоновая проверка, при старте — через полминуты.
	a.startUpdates()
	a.updateRoutes(mux)
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" || r.Header.Get("Authorization") != "Bearer "+endpoint.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	return server, nil
}
func (a *App) Done() <-chan struct{} { return a.ctx.Done() }
