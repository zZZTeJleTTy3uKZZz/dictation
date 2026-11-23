// Ключи распознавания — в общем хранилище ядра (conn.SecretStore).
//
// ЗАЧЕМ. Ключ Groq лежал в providers.json полем protected_key, зашифрованным
// Windows (DPAPI), а ключ своего провайдера — открытым текстом в voice.json.
// Два способа хранения одного секрета — это две дыры, и вторая настоящая:
// voice.json пересылают. Теперь ключи лежат в хранилище ядра (conn.DPAPIStore на
// Windows: тот же DPAPI, только под своим файлом и с замком), а настройки —
// запись провайдера без секрета.
//
// КЛЮЧ СТРОГО ПО АДРЕСУ. Ключ ищется по имени, а имя строится из адреса
// (KeyForURL(NormalizeBaseURL(адрес))). У Groq имя своё — в ссылке провайдер
// так и называется. У своего сервиса имя — узел и путь адреса, приведённые к
// одному виду: два провайдера на одном узле с разными ключами не затирают друг
// друга, а смена адреса оставляет прежний ключ прежнему сервису.
//
// ОТКРЫТОЕ ПОЛЕ voice.SpeechKey — ТОЛЬКО ИСТОЧНИК МИГРАЦИИ. Оно когда-то лежало
// в voice.json открытым текстом; теперь его читает лишь перенос в хранилище, и
// на выбор ключа оно не влияет никогда — хранилище главнее. После успешного
// переноса поле стирается.
//
// МИГРАЦИЯ БЕЗ ПОТЕРИ. Порядок для каждого ключа один:
//
//	прочитать из старого места → записать в хранилище → прочитать обратно и
//	сверить → и только теперь стереть старое.
//
// Оборвись что угодно посреди этого, ключ остаётся хотя бы в одном месте, а
// следующая попытка доведёт дело до конца. Расхождение (в хранилище уже другой
// ключ под тем же именем) старое место НЕ стирает: стирать пришлось бы чужой
// ключ, а он может понадобиться человеку. Битый providers.json — ошибка
// миграции: ничего не стирается и не переписывается.
//
//	providers.json: providers[].protected_key (Groq) → ключ Groq;
//	providers.json: dictation.proxy.protected_password → пароль прокси;
//	voice.json: speech_key (свой провайдер) → ключ своего провайдера.
package speech

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/speech/conn"
	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/atomicfile"
	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/dpapi"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/appdirs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/providercfg"
)

// errSecretNotFound — нет такого секрета.
var errSecretNotFound = errors.New("секрет не найден")

// Хранилище ключей. Файл лежит рядом с настройками диктовки.
const (
	secretsFile = "speech-secrets.json"
	// groqSecret — имя ключа Groq: в ссылке провайдер называется Groq.
	groqSecret = "Groq"
)

// secretsMu — миграция и чтение одного хранилища подряд: два одновременных
// разговора иначе стирали бы прежний ключ, прочитав его же.
var secretsMu sync.Mutex

// migrated — когда хранилище последний раз перебирали: путь → время. Повтор не
// раньше migratedAgain: запись на каждый взгляд окна превратила бы чтение
// настроек в переписывание файлов.
var migrated = map[string]time.Time{}

// migratedAgain — как часто повторять неудавшийся перенос, пока он не удастся.
// Дольше ждать незачем (человек чинит настройки и нажимает «ещё раз»), а
// короче — значило бы переписывать файл на каждое открытие окна.
var migratedAgain = 30 * time.Second

// Подмены для проверок: ими по отдельности имитируются сбой на записи, на
// сверке и на стирании, а также подменный шифр на платформе без DPAPI.
var (
	// storeForMigration — хранилище, в которое переносит миграция.
	storeForMigration func(path string) conn.SecretStore = secretStore
	// newStore — хранилище, из которого всё остальное читает и в которое пишет.
	newStore func(path string) conn.SecretStore = secretStore
	// storePath — как называется файл хранилища для пути настроек.
	storePath = storeFileName
)

// secretLog — куда сказать о неудавшемся или неоднозначном переносе. По
// умолчанию — никуда: библиотека не выбирает за программу её журнал. Служба
// диктовки подключает свой (SetLog), иначе строка о расхождении ключей, которую
// человеку и нужно прочесть, никуда бы не попала.
//
// Значения ключа в строках нет никогда.
var (
	logMu     sync.Mutex
	secretLog func(line string)
	// logRepeat — когда повторить строку журнала, уже сказанную: раз в сутки.
	// Не чаще — потому что это тот же журнал, а не отдельное окно; и повторять,
	// а не замолкать навсегда — потому что первая строка уйдёт вместе с
	// очисткой журнала, а расхождение останется.
	logRepeat = 24 * time.Hour
	// logged — сроки повтора строк, уже сказанных в этом процессе.
	logged = map[string]time.Time{}
)

// SetLog — подключить журнал строк о переносе ключей. Строка уходит туда не
// чаще раза в logRepeat: расхождение — состояние, и повторять его на каждом
// взгляде окна в настройки незачем.
func SetLog(log func(line string)) {
	logMu.Lock()
	secretLog = log
	logMu.Unlock()
}

// SetStore подменяет хранилище ключей на время проверки: она имитирует сбой
// чтения, записи и стирания у настоящего хранилища, а не у подделки в памяти.
// Функция возвращает прежнюю подмену, чтобы её можно было вернуть.
//
// Для программы это не нужно: там хранилище одно — то, что выбрано по системе.
func SetStore(forCheck func(path string) conn.SecretStore) (restore func()) {
	beforeNew, beforeMigration := newStore, storeForMigration
	if forCheck == nil {
		newStore, storeForMigration = secretStore, secretStore
	} else {
		newStore, storeForMigration = forCheck, forCheck
	}
	return func() { newStore, storeForMigration = beforeNew, beforeMigration }
}

// StoreFor — настоящее хранилище ключей для папки настроек path: то же, с
// которым работает программа (DPAPI на Windows, файл на прочих системах).
// Нужно тому, кто подменяет хранилище на время проверки и хочет, чтобы внутри
// подмены лежало настоящее.
func StoreFor(path string) conn.SecretStore { return secretStore(path) }

// ForgetMigrations — забыть, какие хранилища уже перебрали, и что уже сказано
// в журнал: следующий взгляд переберёт заново. Нужно проверке со своим домом —
// иначе перебор, сделанный в прошлой проверке, повторится не раньше срока.
func ForgetMigrations() {
	secretsMu.Lock()
	migrated = map[string]time.Time{}
	secretsMu.Unlock()
	logMu.Lock()
	logged = map[string]time.Time{}
	logMu.Unlock()
}

// sayOnce — сказать в журнал строку не чаще раза в logRepeat. Замок здесь свой:
// журнал может в ответ на строку достать и настройки.
func sayOnce(line string) {
	logMu.Lock()
	defer logMu.Unlock()
	if secretLog == nil {
		return
	}
	if at, done := logged[line]; done && time.Since(at) < logRepeat {
		return
	}
	logged[line] = time.Now()
	secretLog(line)
}

// SecretsPath — файл хранилища ключей распознавания: его видит миграция и им
// проверяется, что прежние ключи уже перенесены.
func SecretsPath() string { return filepath.Join(DictationDir(), secretsFile) }

// storeFileName — файл хранилища для папки настроек path.
func storeFileName(path string) string {
	if path != "" && os.Getenv("DICTATION_SETTINGS_DIR") == "" {
		// Проверки подставляют свой дом через USERPROFILE, а файл секретов
		// должен лежать в нём же, а не в настоящем доме человека.
		file := filepath.Join(filepath.Dir(path), secretsFile)
		appdirs.Guard(file)
		return file
	}
	return SecretsPath()
}

// secretStore — хранилище ключей для папки настроек path.
//
// Windows — DPAPI текущего человека (как и раньше): файл, унесённый на другую
// машину, не расшифровывается. Прочие системы DPAPI не знают, и там ключ лежит
// файлом 0600 (conn.FileStore) — тем же, чем живут проверки и CLI.
func secretStore(path string) conn.SecretStore {
	file := storePath(path)
	if runtime.GOOS == "windows" {
		return conn.DPAPIStore{Path: file}
	}
	return conn.FileStore{Path: file}
}

// unprotect = unprotectKey — прежний ключ забирается тем же шифром, каким его
// кладла прежняя версия. Отдельной переменной — чтобы проверка могла подставить
// свой шифр на платформе без DPAPI и прогнать весь перенос, а не только ту
// половину, где ключ не нужно расшифровывать.
var unprotect = unprotectKey

// providerSecretName — имя ключа провайдера в хранилище по адресу как он
// записан. Адрес приводится к одному виду здесь же: «https://Router.net/v1/» и
// «https://router.net/v1/audio/transcriptions» — один и тот же провайдер, и
// ключ у него один.
//
// Groq — узнаваемое имя; свой провайдер — узел и путь его адреса. Пустой
// адрес — ключа нет: у ключа без адреса нет провайдера, которому он
// принадлежит.
//
// ИМЯ СКЛЕЕНО БЕЗ РАЗДЕЛИТЕЛЯ: «узел + путь». Отсюда один безобидный случай:
// адрес «example.net» с путём «/net/v1» и адрес «example.net/net» с путём «/v1»
// дают одно имя «example.net/net/v1». Такого стечения (совпавших адресов И
// одинакового ключа) не бывает у настоящих сервисов, а разделитель
// («узел|путь») занял бы место, которое бывает в имени узла, и коллизий не
// стало бы меньше, а больше. Здесь это записано, чтобы случай не искали заново.
func providerSecretName(baseURL string) string {
	address := strings.TrimSpace(baseURL)
	if address == "" {
		// Пустой адрес — это адрес Groq по умолчанию: диктовка с выбранным
		// Groq и пустым speech_url ходит именно туда (в ссылке провайдер так и
		// называется). Раньше здесь было «ключа некуда положить», и ключ,
		// вписанный при пустом адресе, не записывался вовсе, а открытая копия
		// при этом стиралась. Канонизация — ЗДЕСЬ, до всех, кто строит имя:
		// иначе один и тот же ключ лёг бы под разными именами.
		return groqSecret
	}
	if normalized, err := providercfg.NormalizeBaseURL(address); err == nil {
		address = normalized
	}
	if isGroq(address) {
		// У Groq имя одно, и адрес Groq и запись провайдера groq-speech дают
		// его же: ключ не должен переселяться под «узел + путь».
		return groqSecret
	}
	host := providercfg.HostOf(address)
	if host == "" {
		return ""
	}
	return host + addressPath(address)
}

// addressPath — путь адреса без слэша на конце: https://узел/v1 → /v1,
// https://узел → "".
func addressPath(address string) string {
	rest := address
	if at := strings.Index(rest, "://"); at >= 0 {
		rest = rest[at+3:]
	}
	if at := strings.IndexAny(rest, "?#"); at >= 0 {
		rest = rest[:at]
	}
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return ""
	}
	return strings.TrimRight(rest[slash:], "/")
}

// isGroq — тот ли это сервис: сравниваются узлы адресов, а не строки. Пустой
// адрес — тоже Groq: он и есть адрес по умолчанию.
func isGroq(baseURL string) bool {
	if strings.TrimSpace(baseURL) == "" {
		return true
	}
	host := providercfg.HostOf(baseURL)
	return host != "" && host == providercfg.HostOf(GroqBaseURL)
}

// ensureMigrated — перенести прежние ключи в хранилище, не чаще раза в
// migratedAgain на путь.
func ensureMigrated(path string) {
	if path == "" {
		path = ProvidersPath()
	}
	secretsMu.Lock()
	if at, done := migrated[path]; done && time.Since(at) < migratedAgain {
		secretsMu.Unlock()
		return
	}
	// Ошибки миграции работе не мешают: прежнее место читается на месте, и
	// ключ не теряется. В журнал — уже без замка миграции и не чаще раза в
	// logRepeat: расхождение повторилось бы на каждом взгляде окна.
	//
	// СРОК — ТОЛЬКО У УДАЧИ. Неудачный перенос не считается перебором: иначе
	// «перебрано» держалось бы на нём, и ключ, вписанный сразу после, не
	// находился бы до следующего срока (а без нового взгляда — вовсе). Следующая
	// попытка после ошибки — на следующем же чтении.
	err := migrateSecrets(path)
	if err == nil {
		migrated[path] = time.Now()
	}
	secretsMu.Unlock()
	if err != nil {
		sayOnce("speech-migration: " + err.Error())
	}
}

// Migrate — перебрать настройки и перенести прежние ключи в хранилище прямо
// сейчас, не дожидаясь начала приложения.
//
// Обычно это происходит само при первом чтении ключа (ensureMigrated, не чаще
// раза в migratedAgain). Программе это нужно лишь там, где она решает «ключа
// нет» до первого чтения, а читать его тут же и собирается.
func Migrate(path string) {
	if path == "" {
		path = ProvidersPath()
	}
	ensureMigrated(path)
}

// migrateSecrets — забрать прежние ключи из providers.json и voice.json.
//
// Порядок для каждого ключа один (см. carrySecret): записали — сверили —
// стёрли. Оборвись что угодно посреди этого, ключ останется хотя бы в одном
// месте, а следующая попытка доведёт дело до конца.
//
// Возвращается ПЕРВАЯ ошибка, и она — причина не считать перебор сделанным
// (ensureMigrated): ключ, который не перенёсся, попробуют снова на следующем
// чтении, а не через срок. Остальные ключи при этом переносятся: один
// недоступный секрет не держит остальные в старом месте.
func migrateSecrets(path string) error {
	store := storeForMigration(path)
	var first error
	note := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}

	// ── providers.json: Groq и пароль прокси ──────────────────────────────
	// Битый файл — ошибка миграции: ключ может лежать в нём, а что ещё в нём
	// лежит, нам не видно; переписать его значило бы потерять словарь и записи.
	whole, err := readWhole(path)
	if err != nil {
		return err
	}
	changed := false
	providers, _ := whole["providers"].([]any)
	for _, one := range providers {
		saved, ok := one.(map[string]any)
		if !ok {
			continue
		}
		sealed := textOf(saved["protected_key"])
		if sealed == "" {
			continue
		}
		name := providerSecretName(textOf(saved["base_url"]))
		if name == "" && saved["id"] == GroqProviderID {
			name = groqSecret
		}
		if name == "" {
			continue
		}
		key, err := unprotect(sealed)
		if err != nil {
			// Чужое место ключа (файл принесли с другой машины): прежнее не
			// трогаем, человек впишет ключ заново.
			continue
		}
		if err := carrySecret(store, name, key); err != nil {
			// Не записали, не сверили или там уже другой ключ — в любом из
			// этих случаев прежнее место остаётся.
			note(err)
			continue
		}
		delete(saved, "protected_key")
		changed = true
	}
	dictation, _ := whole["dictation"].(map[string]any)
	proxy, _ := dictation["proxy"].(map[string]any)
	if proxy != nil {
		if sealed := textOf(proxy[proxyPasswordField]); sealed != "" {
			if password, err := unprotect(sealed); err == nil {
				if err := carrySecret(store, proxyPasswordSecret, password); err == nil {
					delete(proxy, proxyPasswordField)
					changed = true
				} else {
					note(err)
				}
			}
		}
	}
	if changed {
		// Пишем БЕЗ хранилища: запись providers.json — не повод достать ключи
		// снова, и из-под замка переноса этому месту некуда идти.
		if err := writeWholeSettings(path, whole); err != nil {
			return err
		}
	}

	// ── voice.json: ключ своего провайдера ───────────────────────────────
	note(migrateVoiceKey(store, prefs.VoicePath()))
	return first
}

// migrateVoiceKey — ключ своего провайдера из voice.json.
//
// Ключ переезжает в хранилище и исчезает из voice.json: файл пересылают и
// показывают, и секрету там не место. Стирается он только после того, как
// хранилище его отдало — то же правило, что у остальных ключей.
func migrateVoiceKey(store conn.SecretStore, voicePath string) error {
	voice := prefs.LoadVoice(voicePath)
	key := strings.TrimSpace(voice.SpeechKey)
	if key == "" {
		return nil
	}
	name := providerSecretName(voice.SpeechURL)
	if name == "" {
		// Адреса нет — ключу некуда переехать: он остаётся в voice.json, где
		// лежал, и уйдёт в хранилище, когда человек впишет адрес.
		return nil
	}
	if err := carrySecret(store, name, key); err != nil {
		return err
	}
	// Шло по адресу, который считали при переносе: стереть прочитанное можно
	// только из того самого места, а не из ленивого умолчания «voice.json
	// где-то там».
	return eraseLegacyField(voicePath, func(stored *prefs.Voice) bool {
		stored.SpeechKey = ""
		return true
	})
}

// carrySecret — записать ключ в хранилище, сверить и только потом считать
// прежнее место перенесённым.
//
// В хранилище пусто — пишем и сверяем чтением. В хранилище тот же ключ —
// вернуть можно, ничего не теряя. В хранилище ДРУГОЙ ключ под тем же именем —
// прежнее место не трогаем: стерев его, мы потеряли бы ключ, который у
// человека был, а какой из двух верный, знает только он. В журнал уходит строка
// без значения ключа.
func carrySecret(store conn.SecretStore, name, value string) error {
	value = strings.TrimSpace(value)
	if name == "" || value == "" {
		return errors.New("нечего переносить")
	}
	stored, err := store.Get(name)
	switch {
	case err == nil && stored == value:
		return nil
	case errors.Is(err, conn.ErrSecretNotFound):
		// Пустое место: пишем ниже.
	case err == nil && stored != "":
		// В журнал — не здесь, а у того, кто звал перенос: ошибка всё равно
		// попадает туда строкой, а «сказано однажды» считается там же.
		return fmt.Errorf("%w (под именем %q): прежнее место оставлено — какой из двух верный, решает человек",
			errKeyDiffers, name)
	case err == nil:
		// Пустой секрет — пишем как в пустое.
	default:
		// Ошибка чтения — не повод писать вслепую: под тем же именем может
		// лежать другой ключ, и затереть его значило бы потерять ключ человека.
		return err
	}
	if err := store.Set(name, value); err != nil {
		return fmt.Errorf("ключ %q не записан в хранилище: %w", name, err)
	}
	if back, err := store.Get(name); err != nil || back != value {
		return fmt.Errorf("ключ %q не сошёлся при обратном чтении", name)
	}
	return nil
}

// registerProxyPassword — записать пароль прокси. Перенос прежних ключей идёт
// здесь, а не в записи настроек: см. SaveProxy.
func registerProxyPassword(value, path string) error {
	if path == "" {
		path = ProvidersPath()
	}
	ensureMigrated(path)
	return insertSecret(newStore(path), proxyPasswordSecret, value)
}

// insertSecret — записать секрет мимо проверки на расхождение: его владелец
// (человек через окно) говорит прямо «вот новый», и прежнее под тем же именем
// заменяется, а не остаётся спором. Чтение при этом записывать не мешает:
// не читается — пишем, оно же не «там другой ключ».
func insertSecret(store conn.SecretStore, name, value string) error {
	if name == "" || strings.TrimSpace(value) == "" {
		return errEmptyKey
	}
	return store.Set(name, value)
}

// errKeyDiffers — в хранилище под тем же именем уже другой ключ.
var errKeyDiffers = errors.New("под тем же именем в хранилище уже другой ключ")

// eraseLegacyField — перечитать настроечный файл и переписать его без прежнего
// поля.
//
// Перечитывание обязательно: между переносом и стиранием файл могли переписать
// (человек вписал в окне другой ключ), и запись по зачитанной копии молча
// откатила бы его правку. Если сохранить не выйдет, файл возвращается как был:
// наполовину изменённых настроек не бывает.
func eraseLegacyField(path string, change func(*prefs.Voice) bool) error {
	before, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	voice := prefs.LoadVoice(path)
	if !change(voice) {
		return nil
	}
	if err := voice.Save(); err != nil {
		if readErr == nil {
			_ = writeAtomic(path, before)
		}
		return fmt.Errorf("ключ перенесён, но прежнее место не стёрто: %w", err)
	}
	return nil
}

// secretOf — секрет из хранилища: nil — есть, errSecretNotFound — нет, прочее —
// хранилище не прочиталось.
func secretOf(name, path string) error {
	if name == "" {
		return errSecretNotFound
	}
	ensureMigrated(path)
	value, err := newStore(path).Get(name)
	switch {
	case err == nil && strings.TrimSpace(value) != "":
		return nil
	case err == nil:
		// Пустое место — то же, что его нет: назначенным паролем прокси пустая
		// строка не считается.
		return errSecretNotFound
	case errors.Is(err, conn.ErrSecretNotFound):
		return errSecretNotFound
	default:
		return err
	}
}

// secretValue — секрет из хранилища; пусто — его нет.
func secretValue(name, path string) string {
	if name == "" {
		return ""
	}
	ensureMigrated(path)
	value, err := newStore(path).Get(name)
	if err != nil {
		return ""
	}
	return value
}

// setSecret — записать секрет в хранилище.
func setSecret(name, value, path string) error {
	if path == "" {
		path = ProvidersPath()
	}
	ensureMigrated(path)
	return newStore(path).Set(name, value)
}

// deleteSecret — стереть секрет. Отсутствующий — не ошибка.
func deleteSecret(name, path string) error {
	if name == "" {
		return nil
	}
	if path == "" {
		path = ProvidersPath()
	}
	ensureMigrated(path)
	return newStore(path).Delete(name)
}

// Key — ключ провайдера для запроса: есть ли он и чем он отдаст отказ.
//
// Один разбор на всю программу: если ключа нет, запрос не уходит вовсе, и
// человеку называется причина, а не отказ сервиса. Адрес по умолчанию — адрес
// провайдера; нет записи — Groq (у своей записи без адреса ключа нет вовсе).
func Key(path, providerID string, hosts []string) ProviderKey {
	if path == "" {
		path = ProvidersPath()
	}
	ensureMigrated(path)
	registered, ok := RegisteredProvider(path, providerID, hosts)
	base := registered.BaseURL
	if !ok {
		base = GroqBaseURL
		if providerID != GroqProviderID {
			base = ""
		}
	}
	return readKey(newStore(path), providerSecretName(base), base)
}

// KeyForURL — ключ для адреса: Groq — свой, прочий — по узлу и пути адреса.
// Адрес приводится к одному виду: «/v1/» и «/v1/audio/transcriptions» — один
// провайдер. Пустой адрес — у ключа нет провайдера.
func KeyForURL(path, baseURL string) ProviderKey {
	if path == "" {
		path = ProvidersPath()
	}
	ensureMigrated(path)
	return readKey(newStore(path), providerSecretName(baseURL), baseURL)
}

// ProviderKey — что мы знаем о ключе провайдера.
type ProviderKey struct {
	// Key — сам ключ. Наружу (окну, журналу) он не отдаётся; здесь он нужен
	// только тому, кто собирает запрос.
	Key string
	// Name — под каким именем ключ лежит в хранилище.
	Name string
	// Why — почему ключа нет, словами для человека. Пусто — ключ есть.
	Why string
}

// Set — ключ задан.
func (k ProviderKey) Set() bool { return strings.TrimSpace(k.Key) != "" }

// readKey — прочитать ключ из хранилища и назвать причину, если его нет.
func readKey(store conn.SecretStore, name, baseURL string) ProviderKey {
	found := ProviderKey{Name: name}
	if name == "" {
		found.Why = "у своего провайдера не вписан адрес — ключ некуда положить"
		return found
	}
	value, err := store.Get(name)
	switch {
	case err == nil && strings.TrimSpace(value) != "":
		found.Key = value
		return found
	case errors.Is(err, conn.ErrSecretNotFound):
		found.Why = "ключ не зарегистрирован — его вписывают в настройках диктовки"
	case err != nil:
		// Ошибка чтения — ЭТО НЕ «КЛЮЧА НЕТ»: он, скорее всего, лежит в
		// хранилище, но эта учётная запись его не видит (файл принесли с другой
		// машины, шифр не тот). Отличить её от отсутствия обязательно (CheckKey,
		// KeyReadable) — по ней ключ не стирают и не считают чужим. Слова же
		// человеку — прежние: это место читает и первый запуск, и «не читается»
		// там было бы непонятно.
		found.Why = "ключ не читается этой учётной записью Windows — впишите заново"
	default:
		found.Why = "у записи нет ключа"
	}
	if isGroq(baseURL) {
		found.Why = "впишите ключ Groq (он начинается с gsk" + "_)"
	}
	return found
}

// probeKey — прочитать ключ, назвав, ПОЧЕМУ его нет: nil — есть,
// errSecretNotFound — ключа нет или у адреса нет имени ключа, прочее —
// хранилище не прочиталось.
//
// Это та же разница, что у secretOf, но от лица ключа по адресу: сверка и
// запись ключа сверяются по ней, а не по KeyForURL — тот отдаёт пустой ключ и
// на «ключа нет», и на «ключ не читается», и по нему сбой чтения неотличим от
// расхождения.
func probeKey(store conn.SecretStore, name, baseURL string) (string, error) {
	if name == "" {
		return "", errSecretNotFound
	}
	value, err := store.Get(name)
	switch {
	case errors.Is(err, conn.ErrSecretNotFound):
		return "", errSecretNotFound
	case err != nil:
		return "", err
	case strings.TrimSpace(value) == "":
		return "", errSecretNotFound
	}
	return value, nil
}

// readKeyStrict — ключ для адреса без мягких умолчаний: ошибка чтения
// хранилища возвращается как есть, а не превращается в «ключа нет».
func readKeyStrict(path, baseURL string) (string, error) {
	if path == "" {
		path = ProvidersPath()
	}
	ensureMigrated(path)
	return probeKey(newStore(path), providerSecretName(baseURL), baseURL)
}

// unprotectKey — прочитать прежний защищённый ключ (providers.json до переезда).
func unprotectKey(value string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(data) == 0 {
		return "", errors.New("ключ недоступен")
	}
	plain, err := dpapi.Unprotect(data)
	if err != nil {
		return "", errors.New("ключ недоступен для этой учётной записи Windows")
	}
	return string(plain), nil
}

// writeFile — записать файл настроек целиком через временный файл рядом: половина
// настроек хуже их отсутствия, а служба диктовки читает этот файл на ходу.
// Переменная, а не функция: проверки имитируют сбой записи, подменяя её.
var writeFile = writeAtomic

// writeAtomic — запись файла целиком или никак: временный файл рядом, сброс на
// диск, переименование поверх прежнего.
func writeAtomic(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return atomicfile.Write(path, data, 0o600)
}

// writeWholeSettings — записать providers.json целиком, не трогая хранилище
// ключей. Внутри — всё, что делает writeWhole, кроме записи секретов.
//
// ЗАЧЕМ ОТДЕЛЬНАЯ. writeWhole (его же зовёт SaveProxy) раньше писал пароль
// прокси в хранилище — и на сбой чтения выходил в перенос, а перенос зовёт
// writeWhole: два входа в один замок на одном потоке, то есть вечная
// остановка. Из-под замка переноса запись настроек хранилища не касается.
func writeWholeSettings(path string, whole map[string]any) error {
	if path == "" {
		path = ProvidersPath()
	}
	written, err := json.MarshalIndent(whole, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(written, '\n'))
}

// GroqKey — ключ Groq из хранилища.
func GroqKey(path string) ProviderKey {
	return Key(path, GroqProviderID, nil)
}

// RegisterGroqKey — вписать ключ Groq в хранилище.
func RegisterGroqKey(path, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errEmptyKey
	}
	return setSecret(groqSecret, key, path)
}

// SaveGroqProvider — завести (или обновить) в providers.json запись провайдера
// Groq: его адрес и имя. Ключ сюда не пишется — он в хранилище.
//
// ЗАЧЕМ. Ключ Groq вписывают одно поле, но облако «Groq» без записи провайдера
// не считается заведённым: запись — то, по чему окно и служба видят, что
// провайдер есть, и без неё ключ в хранилище ни на что не опирается.
func SaveGroqProvider(path string) error {
	if path == "" {
		path = ProvidersPath()
	}
	whole, err := readWhole(path)
	if err != nil {
		return err
	}
	list, _ := whole["providers"].([]any)
	found := false
	for _, one := range list {
		saved, ok := one.(map[string]any)
		if !ok || saved["id"] != GroqProviderID {
			continue
		}
		saved["base_url"] = GroqBaseURL
		// Прежнее защищённое поле ключа здесь убирается не случайно: ключ уже
		// перенесён в хранилище переносом, а запись без него — та же запись.
		found = true
	}
	if !found {
		list = append(list, map[string]any{
			"id":       GroqProviderID,
			"base_url": GroqBaseURL,
		})
	}
	whole["providers"] = list
	// Настройки пишутся без хранилища: перенос прежних ключей идёт на чтении,
	// и входить сюда из-под его замка этому месту не с кем.
	return writeWholeSettings(path, whole)
}

// ProviderSecretName — под каким именем в хранилище лежит ключ для этого
// адреса: узел и путь адреса, а у Groq — своё имя (в ссылке провайдер
// называется Groq). Пусто — у ключа нет провайдера, которому он принадлежит
// (пустой адрес).
func ProviderSecretName(baseURL string) string { return providerSecretName(baseURL) }

// SetKey — записать ключ по имени в хранилище (имя — из ProviderKey.Name).
func SetKey(name, value string) error {
	if strings.TrimSpace(name) == "" {
		return errEmptyKey
	}
	if strings.TrimSpace(value) == "" {
		return errEmptyKey
	}
	return setSecret(name, value, "")
}

// SetKeyForURL — записать ключ строго по адресу: имя ключа строится из адреса,
// а не берётся у вызывающего. Возвращает имя, под которым ключ лёг.
func SetKeyForURL(path, baseURL, value string) (string, error) {
	name := providerSecretName(baseURL)
	if name == "" {
		return "", errNoAddress
	}
	if strings.TrimSpace(value) == "" {
		return "", errEmptyKey
	}
	return name, setSecret(name, value, path)
}

// SetKeyChecked — записать ключ по адресу и прочитать обратно: запись, которую
// хранилище не отдало назад, — не запись. Возвращает имя ключа.
//
// СВЕРКА РАЗЛИЧАЕТ ТРИ ИСХОДА, и третий — ошибка чтения — не повод ни стирать
// прежнюю копию ключа, ни считать ключ чужим:
//
//	прочитался тот же ключ       — запись есть;
//	ключа нет                    — запись не легла;
//	хранилище не прочиталось     — неизвестно, легла ли, и почему чтение
//	                               отказало (файл принесли с другой машины,
//	                               шифр не тот). Места ключа это не касается:
//	                               прежнее оставляют, спор с ним не начинают.
//
// Отсюда два вида ошибки сверки: ErrKeyUnreadable — хранилище не прочиталось
// (вызывающий не стирает прежнее и не переключает адрес — не он виноват),
// errKeyNotStored — записи нет, и прежнее можно разбирать как прежде.
func SetKeyChecked(path, baseURL, value string) (string, error) {
	name, err := SetKeyForURL(path, baseURL, value)
	if err != nil {
		return "", err
	}
	if err := CheckKey(path, baseURL, value); err != nil {
		return name, err
	}
	return name, nil
}

// CheckKey — сошлась ли запись ключа по адресу при обратном чтении.
//
// Пусто — сошлась. Иначе: ErrKeyUnreadable, если хранилище не прочиталось;
// ошибка с фразой для человека, если ключ записался, но не тот или вовсе не
// записался.
func CheckKey(path, baseURL, value string) error {
	value = strings.TrimSpace(value)
	back, err := readKeyStrict(path, baseURL)
	switch {
	case err == nil && back == value:
		return nil
	case errors.Is(err, errSecretNotFound):
		return errors.New("ключ не лёг в хранилище — впишите его ещё раз")
	case err != nil:
		return fmt.Errorf("%w: %v — прежнее место ключа не тронуто", ErrKeyUnreadable, err)
	}
	return errors.New("ключ в хранилище не тот, что записали — впишите его ещё раз")
}

// KeyReadable — читается ли хранилище для ключа этого адреса.
//
// Ложь значит «ключ может быть и есть, но не виден этой учётной записи». По ней
// и только по ней отличают «ключа нет» от «ключа не видно»; у KeyForURL оба
// случая выглядят одинаково — пустым ключом.
func KeyReadable(path, baseURL string) bool {
	if path == "" {
		path = ProvidersPath()
	}
	if providerSecretName(baseURL) == "" {
		return true
	}
	_, err := readKeyStrict(path, baseURL)
	return err == nil || errors.Is(err, errSecretNotFound)
}

// ErrKeyUnreadable — хранилище ключей не прочиталось. Это не «ключа нет» и не
// «ключ чужой»: ключ, скорее всего, на месте, но этой учётной записи не виден.
var ErrKeyUnreadable = errors.New("хранилище ключей не читается этой учётной записью")

// errEmptyKey — пустой ключ: это не ключ.
var errEmptyKey = errors.New("ключ пуст — нечего записывать")

// errNoAddress — у ключа нет адреса: ему некуда приписаться.
var errNoAddress = errors.New("сначала впишите адрес API — ключ хранится вместе с адресом")
