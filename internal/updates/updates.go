// Package updates — обновление «Диктовки» по протоколу update/1.
//
// Проверку манифеста, разбор его полей и решение «есть ли новее» делает
// mybits/selfupdate: здесь то, чего у пакета нет, — отдельные шаги «скачать» и
// «поставить». Окну нужен готовый файл, который можно поставить по нажатию, а
// во время диктовки установку приходится откладывать, поэтому
// скачивание и установка живут разными действиями HTTP API.
//
// Контракт: протокол обновлений «Диктовки» (update/1).
package updates

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/selfupdate"
)

// Статусы карточки обновления — ровно восемь состояний §6 протокола.
const (
	StatusIdle        = "idle"
	StatusChecking    = "checking"
	StatusUpToDate    = "up_to_date"
	StatusAvailable   = "available"
	StatusDownloading = "downloading"
	StatusReady       = "ready"
	StatusInstalling  = "installing"
	StatusFailed      = "failed"
)

// AppName — имя приложения в манифесте (§2). Чужой манифест не принимается.
const AppName = "meetdigest-dictation"

// ManifestURL — откуда по умолчанию берётся манифест выпуска.
//
// Одна константа сборки: публичный GitHub Releases, без авторизации. Переменная,
// а не const: сборка может подставить свой адрес через
// -X internal/updates.ManifestURL=…; человеку переопределение не нужно, для
// разработки есть только DICTATION_UPDATE_MANIFEST.
var ManifestURL = "https://github.com/zZZTeJleTTy3uKZZz/dictation/releases/latest/download/update.json"

// Неудачная проверка обновления показывает человеку ровно одну строку. Код
// ошибки и причину уносит Logger в журнал — на экран они не попадают.
const (
	// Нет связи, 401 или 404 при чтении манифеста: источник недоступен.
	checkFailureMessage = "Не удалось проверить обновления — нет связи с GitHub"
	// Сумма архива не сошлась или установщик отказал: связь была, обновление
	// не установилось. Причину человек видит в журнале, а не на экране.
	installFailureMessage = "Обновление не установилось — подробности в журнале"
)

// Причины неудачной проверки манифеста (поле reason). Окно по ним выбирает
// строку для человека; текст Message и CheckError при этом не меняется (§4).
const (
	// ReasonNotPublished — манифест ответил 404: релиз ещё не опубликован.
	ReasonNotPublished = "not_published"
	// ReasonOffline — ответа не было: нет сети, обрыв или таймаут.
	ReasonOffline = "offline"
	// ReasonServerError — сервер ответил, но не успешным кодом (кроме 404).
	ReasonServerError = "server_error"
	// ReasonOther — сбой другого рода: манифест испорчен, чужое приложение и т. п.
	ReasonOther = "other"
)

// State — состояние обновления для окна; поля и коды — по §6 протокола.
type State struct {
	Status string `json:"status"`
	// Version — версия из манифеста (доступная или последняя).
	Version string `json:"version,omitempty"`
	Notes   string `json:"notes,omitempty"`
	// Bytes — размер установщика из манифеста.
	Bytes int64 `json:"bytes,omitempty"`
	// Mandatory — текущий version_code меньше min_version_code манифеста.
	Mandatory bool  `json:"mandatory,omitempty"`
	Received  int64 `json:"received,omitempty"`
	Total     int64 `json:"total,omitempty"`
	// Code, Message и Retryable — состояние failed (§6).
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
	// Deferred — установка отложена: идёт диктовка. Состояние при этом ready:
	// файл готов, ждём конца записи, ничего не убивая.
	Deferred bool `json:"deferred,omitempty"`
	// CheckedAt — когда манифест проверяли в последний раз.
	CheckedAt string `json:"checked_at,omitempty"`
	// CheckError — манифест проверить не удалось, но готовый файл и его
	// состояние остались: сеть не повод терять скачанное обновление.
	CheckError string `json:"check_error,omitempty"`
	// Reason — почему последняя проверка манифеста не удалась (см. Reason*);
	// пусто, когда проверка прошла. Для окна: строка человеку, а не код.
	Reason string `json:"reason,omitempty"`
	// HTTPStatus — код ответа манифеста, если сервер ответил; иначе ноль.
	HTTPStatus int `json:"http_status,omitempty"`
}

// Options — что нужно знать обновлению о программе и её окружении.
type Options struct {
	// ManifestURL — адрес манифеста; пусто — ManifestURL пакета.
	ManifestURL string
	// Token — токен чтения пакета внутреннего реестра; пусто — без него.
	Token string
	// App — имя приложения в манифесте; пусто — AppName.
	App string
	// CurrentVersion и CurrentCode — версия установленной программы.
	CurrentVersion string
	CurrentCode    int64
	// Dir — каталог кэша для скачанного установщика.
	Dir string
	// HTTPClient — для проверок; nil — свой клиент политики §3.
	HTTPClient *http.Client
	// Busy говорит, идёт ли диктовка: установку тогда откладываем, а не рвём.
	Busy func() bool
	// RunInstaller запускает установщик; nil — тихий установщик Windows.
	RunInstaller func(ctx context.Context, path string, asset selfupdate.Asset) error
	// AllUsers говорит, стоит ли программа «для всех» (служба Windows). Тогда
	// установщик получает /ALLUSERS, иначе — /CURRENTUSER. nil — «только для меня».
	AllUsers func() bool
	// Logger пишет события обновления в журнал диктовки.
	Logger func(event, message string)
	// CheckInterval — как часто проверять манифест в фоне; ноль — шесть часов.
	CheckInterval time.Duration
	// InitialDelay — пауза перед первой проверкой при старте; ноль — сразу.
	InitialDelay time.Duration
	// DeferredEvery — как часто смотреть, кончилась ли диктовка, когда
	// установка отложена; ноль — минута.
	DeferredEvery time.Duration
	// Now — часы; nil — time.Now.
	Now func() time.Time
}

// Controller — состояние обновления и действия над ним.
//
// Одно состояние на всю службу: окно спрашивает его GET /update и не хранит
// своей копии, иначе карточка и служба разошлись бы в том, что уже скачано.
type Controller struct {
	opts Options

	mu       sync.Mutex
	state    State
	release  selfupdate.Release
	file     string
	cancel   context.CancelFunc
	checking bool
	running  bool
	baseCtx  context.Context
}

// New собирает контроллер. Сеть и диск он не трогает: проверка начинается
// вызовом Check или Start.
func New(opts Options) *Controller {
	if opts.ManifestURL == "" {
		opts.ManifestURL = ManifestURL
	}
	if opts.App == "" {
		opts.App = AppName
	}
	if opts.Logger == nil {
		opts.Logger = func(string, string) {}
	}
	if opts.CheckInterval <= 0 {
		opts.CheckInterval = 6 * time.Hour
	}
	if opts.DeferredEvery <= 0 {
		opts.DeferredEvery = time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Busy == nil {
		opts.Busy = func() bool { return false }
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = newHTTPClient(opts.Token)
	}
	if opts.Dir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			dir = os.TempDir()
		}
		// Каталог кэша — отдельное имя программы, без названий протокола и сервисов.
		opts.Dir = filepath.Join(dir, "dictation-update")
	}
	return &Controller{opts: opts, state: State{Status: StatusIdle}}
}

// State отдаёт текущее состояние.
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Start запускает фоновую проверку: сразу при старте и потом раз в
// CheckInterval. Пока диктовка идёт, проверка ничего не ломает — манифест
// читается в сети, а не на диске программы.
func (c *Controller) Start(ctx context.Context) {
	c.mu.Lock()
	c.baseCtx = ctx
	c.mu.Unlock()
	go func() {
		if delay := c.opts.InitialDelay; delay > 0 {
			// Не бьём в сеть в ту же секунду, что поднимается служба: пусть
			// сначала поднимется запись, значок и окно.
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		c.Check(ctx)
		check := time.NewTicker(c.opts.CheckInterval)
		defer check.Stop()
		deferred := time.NewTicker(c.opts.DeferredEvery)
		defer deferred.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-check.C:
				c.Check(ctx)
			case <-deferred.C:
				c.installDeferred(ctx)
			}
		}
	}()
}

// Check читает манифест и решает, есть ли версия новее установленной.
//
// Состояние меняется на checking, потом на available / up_to_date / failed.
// Повторная проверка не сбрасывает уже скачанный файл: если версия та же,
// состояние остаётся ready.
func (c *Controller) Check(ctx context.Context) State {
	c.mu.Lock()
	if c.running {
		state := c.state
		c.mu.Unlock()
		return state
	}
	c.checking = true
	// Готовый файл и отложенную установку проверка не сбрасывает: иначе она
	// теряла бы Deferred (и установка после диктовки молча не начиналась), а на
	// время сети карточка показывала бы checking вместо готового файла.
	previous := c.state
	hasFile := c.file != "" && previous.Status == StatusReady
	if !hasFile {
		c.state = State{Status: StatusChecking, CheckedAt: c.opts.Now().UTC().Format(time.RFC3339)}
	}
	c.mu.Unlock()

	// Проверка манифеста — это сеть: даём ей свой предел, чтобы зависший узел
	// не оставлял карточку в «Проверяю…» навсегда.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	src := selfupdate.StaticManifest{URL: c.opts.ManifestURL, HTTPClient: c.opts.HTTPClient}
	rel, newer, err := selfupdate.Check(ctx, src, c.opts.CurrentVersion, selfupdate.CheckOptions{
		OS:          "windows",
		Arch:        "amd64",
		Kinds:       []string{string(selfupdate.KindInstaller), string(selfupdate.KindArchive), string(selfupdate.KindBinary)},
		CurrentCode: c.opts.CurrentCode,
		App:         c.opts.App,
	})

	c.mu.Lock()
	defer c.mu.Unlock()
	c.checking = false
	checked := c.opts.Now().UTC().Format(time.RFC3339)
	if hasFile {
		// Готовый файл уже сверен по размеру и sha256; неудача проверки манифеста
		// его не отменяет. Причина видна отдельно (check_error), а состояние и
		// отложенная установка остаются как были.
		if err != nil {
			c.state = previous
			c.state.CheckError = failureMessage(selfupdate.CodeOf(err))
			c.state.Reason, c.state.HTTPStatus = checkReason(err)
			c.state.CheckedAt = checked
			c.opts.Logger("update-check-failed", err.Error())
			return c.state
		}
		if !newer || sameVersion(c.release.Version, rel.Version) {
			c.state = previous
			c.state.CheckError = ""
			c.state.Reason, c.state.HTTPStatus = "", 0
			c.state.CheckedAt = checked
			return c.state
		}
		// Манифест назвал другую, более новую версию: прежний файл к ней не
		// относится и ставить его нельзя. Скачанное остаётся в кэше, но
		// состояние снова available — окно предложит скачать новое.
		c.file = ""
		c.release = rel
		c.state = State{
			Status:    StatusAvailable,
			Version:   rel.Version,
			Notes:     rel.Notes,
			Bytes:     rel.Asset.Bytes,
			Total:     rel.Asset.Bytes,
			Mandatory: rel.Decision == selfupdate.DecisionMandatory,
			CheckedAt: checked,
		}
		c.opts.Logger("update-available", fmt.Sprintf("%s → %s", c.opts.CurrentVersion, rel.Version))
		return c.state
	}
	if err != nil {
		c.state = failedState(err, checked)
		c.state.CheckError = c.state.Message
		c.state.Reason, c.state.HTTPStatus = checkReason(err)
		c.opts.Logger("update-check-failed", err.Error())
		return c.state
	}
	if !newer {
		c.release = rel
		c.state = State{Status: StatusUpToDate, Version: rel.Version, CheckedAt: checked}
		c.opts.Logger("update-current", rel.Version)
		return c.state
	}
	c.release = rel
	c.state = State{
		Status:    StatusAvailable,
		Version:   rel.Version,
		Notes:     rel.Notes,
		Bytes:     rel.Asset.Bytes,
		Total:     rel.Asset.Bytes,
		Mandatory: rel.Decision == selfupdate.DecisionMandatory,
		CheckedAt: checked,
	}
	c.opts.Logger("update-available", fmt.Sprintf("%s → %s", c.opts.CurrentVersion, rel.Version))
	return c.state
}

// Download скачивает установщик в кэш с прогрессом. Повторный вызов во время
// скачивания ничего не начинает; отмена — Cancel.
func (c *Controller) Download() State {
	c.mu.Lock()
	if c.running || c.release.Asset.URL == "" {
		state := c.state
		c.mu.Unlock()
		return state
	}
	base := c.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	c.cancel = cancel
	c.running = true
	c.state.Status = StatusDownloading
	c.state.Received = 0
	c.state.Code, c.state.Message, c.state.Retryable = "", "", false
	c.state.Deferred = false
	rel := c.release
	state := c.state
	c.mu.Unlock()

	go func() {
		err := c.download(ctx, rel)
		c.mu.Lock()
		c.running = false
		c.cancel = nil
		if err != nil {
			if errors.Is(err, context.Canceled) || selfupdate.CodeOf(err) == selfupdate.CodeCancelled {
				// Отмена — не ошибка: временный файл удалён, можно скачать заново.
				c.state = State{
					Status: StatusAvailable, Version: rel.Version, Notes: rel.Notes,
					Bytes: rel.Asset.Bytes, Total: rel.Asset.Bytes,
					Mandatory: rel.Decision == selfupdate.DecisionMandatory,
					CheckedAt: c.state.CheckedAt,
				}
				c.opts.Logger("update-cancelled", rel.Version)
			} else {
				c.state = failedState(err, c.state.CheckedAt)
				c.opts.Logger("update-download-failed", err.Error())
			}
		} else {
			c.state = State{
				Status: StatusReady, Version: rel.Version, Notes: rel.Notes,
				Bytes: rel.Asset.Bytes, Received: rel.Asset.Bytes, Total: rel.Asset.Bytes,
				Mandatory: rel.Decision == selfupdate.DecisionMandatory,
				CheckedAt: c.state.CheckedAt,
			}
			c.opts.Logger("update-ready", fmt.Sprintf("%s, %d байт", rel.Version, rel.Asset.Bytes))
		}
		c.mu.Unlock()
	}()
	return state
}

// Cancel прерывает скачивание; временный файл удаляется.
func (c *Controller) Cancel() State {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return c.State()
}

// allUsersNotice — что видит человек, когда установка «для всех» временно не
// обновляется из программы.
const allUsersNotice = "Обновление для установки на весь компьютер появится в следующей версии; пока переустановите через install.ps1"

// Install ставит скачанный установщик. Если идёт диктовка, установка
// откладывается до её конца: файл готов, состояние ready + deferred, и фоновая
// нить повторит попытку сама.
func (c *Controller) Install(ctx context.Context) State {
	// Признак читаем до мьютекса: это вызов в Windows.
	allUsers := c.opts.AllUsers != nil && c.opts.AllUsers()
	c.mu.Lock()
	if c.running || c.file == "" {
		state := c.state
		c.mu.Unlock()
		return state
	}
	if allUsers {
		// Временная заглушка (решение владельца): установку «для всех» из
		// программы не запускаем, файл и состояние ready остаются, а человек
		// видит причину и способ обновиться.
		c.state.Deferred = false
		c.state.Message = allUsersNotice
		state := c.state
		c.mu.Unlock()
		c.opts.Logger("update-allusers-unsupported", c.state.Version)
		return state
	}
	if c.opts.Busy() {
		c.state.Deferred = true
		c.state.Message = "Идёт запись: установка начнётся после её конца"
		state := c.state
		c.mu.Unlock()
		c.opts.Logger("update-deferred", c.state.Version)
		return state
	}
	c.running = true
	c.state.Status = StatusInstalling
	c.state.Deferred = false
	c.state.Message = ""
	file := c.file
	rel := c.release
	state := c.state
	c.mu.Unlock()

	run := c.opts.RunInstaller
	if run == nil {
		run = func(ctx context.Context, path string, asset selfupdate.Asset) error {
			return runInstaller(ctx, path, asset, allUsers)
		}
	}
	go func() {
		err := run(ctx, file, rel.Asset)
		c.mu.Lock()
		c.running = false
		c.cancel = nil
		if err != nil {
			c.state = failedState(err, c.state.CheckedAt)
			c.opts.Logger("update-install-failed", err.Error())
		} else {
			// Установщик отработал; новая версия начнёт работать после
			// перезапуска — карточка говорит об этом словами, а не притворяется,
			// что обновление уже в силе.
			c.state = State{
				Status: StatusUpToDate, Version: rel.Version,
				Message:   "Установлено. Перезапустите «Диктовку»",
				CheckedAt: c.state.CheckedAt,
			}
			c.file = ""
			c.opts.Logger("update-installed", rel.Version)
		}
		c.mu.Unlock()
	}()
	return state
}

// installDeferred ставит отложенную установку, когда диктовка кончилась.
func (c *Controller) installDeferred(ctx context.Context) {
	c.mu.Lock()
	deferred := c.state.Deferred && c.file != "" && !c.running
	c.mu.Unlock()
	if !deferred || c.opts.Busy() {
		return
	}
	c.Install(ctx)
}

func sameVersion(a, b string) bool {
	return trimV(a) == trimV(b)
}

func trimV(s string) string {
	if len(s) > 0 && (s[0] == 'v' || s[0] == 'V') {
		return s[1:]
	}
	return s
}

// failureMessage выбирает одну строку для человека по коду ошибки. Код и
// причина уходят в журнал; на экран — только понятное сообщение.
func failureMessage(code selfupdate.Code) string {
	if code == "" || code == selfupdate.CodeDownloadFailed {
		return checkFailureMessage
	}
	return installFailureMessage
}

// statusInError достаёт код ответа из текста ошибки загрузки. selfupdate
// (v0.26.2) отдаёт статус только текстом «GET …: статус NNN», отдельного поля
// у ошибки нет; текст формирует сама библиотека, поэтому разбор привязан к её
// версии в go.mod.
var statusInError = regexp.MustCompile(`статус (\d{3})`)

// checkReason разбирает ошибку проверки манифеста: почему она не удалась и
// какой код ответил сервер (ноль — ответа не было). Для окна это выбор строки:
// «релиз не опубликован», «нет связи», «сервер ответил ошибкой (код)».
func checkReason(err error) (string, int) {
	if err == nil {
		return "", 0
	}
	// Таймаут проверки — это тоже нет связи. Код у него cancelled, поэтому
	// проверяем его раньше кодов протокола.
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonOffline, 0
	}
	if selfupdate.CodeOf(err) != selfupdate.CodeDownloadFailed {
		return ReasonOther, 0
	}
	match := statusInError.FindStringSubmatch(err.Error())
	if match == nil {
		// Ответа не было: обрыв, отказ соединения, DNS.
		return ReasonOffline, 0
	}
	status, convErr := strconv.Atoi(match[1])
	if convErr != nil {
		return ReasonOther, 0
	}
	if status == http.StatusNotFound {
		return ReasonNotPublished, status
	}
	return ReasonServerError, status
}

// failedState переводит ошибку протокола в состояние failed (§6).
//
// Код остаётся в поле Code (и уходит в журнал через Logger), но не показывается
// человеку: 401, 404 и обрыв сети выглядят одной строкой; несошёдшаяся сумма
// и отказ установщика — другой, как решил владелец.
func failedState(err error, checked string) State {
	code := selfupdate.CodeOf(err)
	if code == "" {
		code = selfupdate.CodeDownloadFailed
	}
	return State{
		Status:    StatusFailed,
		Code:      string(code),
		Message:   failureMessage(code),
		Retryable: code.Retryable(),
		CheckedAt: checked,
	}
}
