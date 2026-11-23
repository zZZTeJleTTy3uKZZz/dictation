// Ссылка-конфигурация провайдера распознавания — на общем ядре speech/conn.
//
// ЗАЧЕМ ССЫЛКА. Друг владельца ставит Диктовку и настраивает её сам. Объяснять
// по телефону адрес API, модель и ключ — значит однажды перепутать одну букву и
// час искать, почему «не распознаёт». Ссылка переносит всю настройку одной
// строкой: её присылают в мессенджере, вставляют в поле, и провайдер готов.
//
// ЧТО ЗДЕСЬ ОСТАЛОСЬ. Своего разбора больше нет: ссылку speech-provider://v1/…
// и прежнюю dictation://provider?… разбирает и собирает ядро (conn.Parse,
// conn.Encode, conn.Share), адрес приводит conn.NormalizeBaseURL. Здесь только
// перевод в запись Диктовки: у неё провайдер — это несколько полей voice.json, а
// у ядра — одна запись Provider без ключа. Копия разбора разошлась бы с ядром на
// первой же правке формата — поэтому её нет.
package providercfg

import (
	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/speech/conn"
)

// Link — провайдер, каким его переносит ссылка.
//
// Повторяет conn.Link, чтобы окно и установщик (internal/dictconfig) не знали
// про ядро: им нужны ровно эти четыре поля.
type Link struct {
	// Name — как провайдер называется для человека. Пусто — назовём узлом.
	Name string
	// URL — адрес OpenAI-совместимого API. Хвост /audio/transcriptions можно не
	// писать: он дописывается при запросе.
	URL string
	// Model — модель распознавания.
	Model string
	// Key — ключ. Может отсутствовать: ссылку делятся и без ключа, чтобы не
	// рассылать секрет.
	Key string
}

// Parse разбирает ссылку-конфигурацию: новую speech-provider://v1/… и старую
// dictation://provider?….
//
// Ошибки ядро называет так, чтобы человеку было что исправить: «это не ссылка
// провайдера», «адрес только https», «в ссылке нет модели». Они и уходят в окно
// как есть.
func Parse(raw string) (Link, error) {
	parsed, err := conn.Parse(raw)
	if err != nil {
		return Link{}, err
	}
	return ofProvider(parsed.Provider, parsed.Key), nil
}

// Build собирает ссылку-конфигурацию из настроек провайдера.
//
// Ссылка выпускается только новая (speech-provider://v1/…): старая читается, но
// не выпускается никогда. Ключ попадает в ссылку, только если он передан: по
// умолчанию человек делится настройкой без секрета.
func Build(link Link) (string, error) {
	return conn.Encode(conn.Link{Provider: providerOf(link), Key: link.Key})
}

// Share собирает ссылку для передачи другому человеку. withKey=false (умолчание
// окна) — без ключа: её можно показать, переслать и положить в журнал.
func Share(link Link, withKey bool) (string, error) {
	return conn.Share(providerOf(link), link.Key, withKey)
}

// LinkFormat — подсказка формата ссылки, какой её показывает окно.
//
// Строка, а не описание: её видит человек в поле ввода, и она обязана совпадать
// с тем, что выпускает Build (speech-provider://v1/… — схема и версия из ядра,
// дальше один кусок). Окно берёт её отсюда, чтобы подсказка и настоящая ссылка
// не разошлись на первой правке формата в ядре.
func LinkFormat() string {
	return conn.LinkScheme + "://" + conn.LinkVersion + "/…"
}

// NormalizeBaseURL проверяет адрес API и убирает хвост расшифровки.
//
// Разбор в ядре: адрес без схемы, http в интернет и имя с паролем внутри
// отвергаются там же, где и при разборе ссылки, — одна проверка на всех.
func NormalizeBaseURL(raw string) (string, error) {
	return conn.NormalizeBaseURL(raw)
}

// ValidateBaseURL — проверить адрес API, ничего не начиная.
func ValidateBaseURL(raw string) error {
	_, err := NormalizeBaseURL(raw)
	return err
}

// HostOf — узел адреса: по нему называется провайдер, если имени не дали.
func HostOf(raw string) string { return conn.HostOf(raw) }

// providerOf — запись ядра из полей ссылки. Вид не задаётся: провайдер узнаётся
// по адресу (conn.KindOf), и в ссылку он не пишется.
func providerOf(link Link) conn.Provider {
	return conn.Provider{
		Name:    link.Name,
		BaseURL: link.URL,
		Model:   link.Model,
	}
}

// ofProvider — поля ссылки из записи ядра. Адрес и имя уже приведены ядром:
// пустое имя — узел адреса.
func ofProvider(provider conn.Provider, key string) Link {
	return Link{
		Name:  provider.Name,
		URL:   provider.BaseURL,
		Model: provider.Model,
		Key:   key,
	}
}
