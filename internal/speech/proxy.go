// Прокси для распознавания: хранение и клиент.
//
// ПАРОЛЬ ЛЕЖИТ ХРАНЕНИЕМ КЛЮЧА. Прокси бывает с логином и паролем, а
// providers.json человек может открыть и переслать — открытым текстом пароля в
// нём быть не должно. Поэтому он живёт в том же хранилище ядра (conn.SecretStore,
// на Windows — DPAPI текущего человека), что и ключи: второй способ хранения
// означал бы вторую дыру.
//
// ПОЧЕМУ В ОБЩЕМ ФАЙЛЕ, А НЕ В voice.json. Прокси относится к распознаванию и
// живёт рядом с ключами и адресами сервисов. voice.json общий с Python-версией,
// и новая запись там — это поле, которого чужая половина не знает.
package speech

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/providercfg"
)

// ProxySettings — настройка посредника так, как её видит окно.
type ProxySettings struct {
	URL  string
	User string
	// Password — только при записи. При чтении здесь пусто: наружу пароль не
	// отдаётся, а нужен он лишь тому, кто идёт в сеть. Читает его только
	// RecognitionClient — тем же хранилищем ключа.
	Password string
	// PasswordSet — пароль задан. По нему окно показывает «пароль сохранён», не
	// разбирая сам пароль.
	PasswordSet bool
	// Why — почему пароль не прочитался (например, файл принесли с другой
	// машины). Пусто — всё в порядке.
	Why string
}

// Роящийся пароль прокси: имя секрета в хранилище и в прежнем providers.json.
const (
	proxyPasswordSecret = "dictation-proxy"
	proxyPasswordField  = "protected_password"
)

// ReadProxy — прочитать настройку прокси.
//
// Нет файла, нет раздела, пароль не расшифровывается — это не ошибка загрузки
// настроек: диктовка работает без прокси, а окно покажет, что пароль придётся
// вписать заново.
func ReadProxy(path string) ProxySettings {
	if path == "" {
		path = ProvidersPath()
	}
	whole, err := readWhole(path)
	if err != nil {
		return ProxySettings{}
	}
	dictation, _ := whole["dictation"].(map[string]any)
	proxy, _ := dictation["proxy"].(map[string]any)
	if proxy == nil {
		return ProxySettings{}
	}
	found := ProxySettings{
		URL:  strings.TrimSpace(textOf(proxy["url"])),
		User: strings.TrimSpace(textOf(proxy["user"])),
	}
	switch secretOf(proxyPasswordSecret, path) {
	case nil:
		found.PasswordSet = true
	case errSecretNotFound:
		// Прежний пароль ещё не переехал (файл принесли с другой машины или
		// миграция не дошла): окно скажет, что пароль придётся вписать заново.
		if textOf(proxy[proxyPasswordField]) != "" {
			found.Why = "пароль прокси не читается этой учётной записью Windows — впишите заново"
		}
	default:
		found.Why = "пароль прокси не читается этой учётной записью Windows — впишите заново"
	}
	return found
}

// SaveProxy — записать адрес, имя и пароль.
//
// password == nil значит «пароль не трогать»: окно показывает звёздочки и не
// присылает сохранённый пароль обратно, и затирать его пустым полем нельзя.
// password != nil с пустой строкой — осознанно стереть пароль.
func SaveProxy(path string, settings ProxySettings, password *string) error {
	if path == "" {
		path = ProvidersPath()
	}
	whole, err := readWhole(path)
	if err != nil {
		return err
	}
	dictation, _ := whole["dictation"].(map[string]any)
	if dictation == nil {
		dictation = map[string]any{}
	}
	proxy, _ := dictation["proxy"].(map[string]any)
	if proxy == nil {
		proxy = map[string]any{}
	}
	proxy["url"] = strings.TrimSpace(settings.URL)
	proxy["user"] = strings.TrimSpace(settings.User)
	// Прежнее место пароля убираем из файла: секрет в настройках, которые
	// человек пересылает, — это и была та дыра, ради которой ядро разделяет
	// настройки и хранилище.
	delete(proxy, proxyPasswordField)
	dictation["proxy"] = proxy
	whole["dictation"] = dictation
	// Хранилище ключей здесь не трогается вовсе — ни письмом, ни стиранием.
	//
	// ЗАЧЕМ. Раньше запись шла в хранилище, а оно на сбой чтения могло уйти в
	// перенос прежних ключей, перенос зовёт обратно сюда — и на сбое чтения это
	// два входа в один замок на одном потоке, то есть зависшая служба.
	// Пароль теперь переезжает защищённым через /provider/apply
	// (registerProxyPassword), где переносом управляют явно.
	if password != nil {
		// Поля прокси и пароль ложатся вместе: адрес без пароля — это прокси,
		// который не пустит, и настройки такого вида не сохраняются.
		value := strings.TrimSpace(*password)
		if value == "" {
			if err := deleteSecret(proxyPasswordSecret, path); err != nil {
				return fmt.Errorf("пароль прокси не стёрт: %w", err)
			}
		} else if err := registerProxyPassword(value, path); err != nil {
			return fmt.Errorf("пароль прокси не сохранён: %w", err)
		}
	}
	// Настройки пишутся последними и БЕЗ хранилища: перенос прежних ключей уже
	// случился выше (ensureMigrated в registerProxyPassword), а сюда он уйти не
	// должен — второй вход в замок на этом же потоке.
	return writeWholeSettings(path, whole)
}

// ProxyExit — строка выхода ядра по прокси из настроек: адрес, имя и пароль — в
// адресе, как их понимает транспорт. Ею ходит проверка ключа (conn.Check).
func ProxyExit(path string) string {
	settings := ReadProxy(path)
	settings.Password = secretValue(proxyPasswordSecret, path)
	return proxyOf(settings).Exit()
}

// proxyOf — настройка посредника в том виде, в каком её понимает транспорт
// ядра (providercfg): адрес, а имя и пароль внутри адреса.
func proxyOf(settings ProxySettings) providercfg.Proxy {
	return providercfg.Proxy{URL: settings.URL, User: settings.User, Password: settings.Password}
}

// RecognitionClient — клиент для запросов распознавания: через прокси, если он
// задан, иначе обычный. Возвращает ещё и строку выхода ядра — ею ходит проверка
// ключа (conn.Check), чтобы проверка и диктовка шли одним путём.
//
// Ошибка значит, что адрес прокси не разобрать. Возвращать молча обычный клиент
// нельзя: человек вписал прокси и ждёт, что запрос пойдёт через него, а зелёная
// диктовка шла бы напрямую — и в сети без прямого доступа всё равно не работала
// бы, только причина была бы уже другой.
func RecognitionClient(path string, timeout time.Duration) (*http.Client, string, string) {
	settings := ReadProxy(path)
	if strings.TrimSpace(settings.URL) == "" {
		return &http.Client{Timeout: timeout}, "", ""
	}
	// Пароль читается хранилищем, а не из настроек: окну он не нужен, а тот,
	// кто идёт в сеть, берёт его оттуда же, где он записан.
	settings.Password = secretValue(proxyPasswordSecret, path)
	where := proxyOf(settings)
	client, err := providercfg.NewClient(where, timeout)
	if err != nil {
		return &http.Client{Timeout: timeout}, "", fmt.Sprintf("прокси не используется: %v", err)
	}
	return client, where.Exit(), ""
}

// writeWhole — записать providers.json целиком через временный файл.
//
// Папку заводим сами: на новой машине её ещё нет, а онбординг вписывает первый
// ключ до того, как что-либо успело создать каталог настроек.
func writeWhole(path string, whole map[string]any) error {
	if path == "" {
		path = ProvidersPath()
	}
	written, err := json.MarshalIndent(whole, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(written, '\n'))
}

// textOf — значение поля настроек строкой.
func textOf(value any) string {
	text, _ := value.(string)
	return text
}
