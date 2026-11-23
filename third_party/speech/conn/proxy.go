package conn

import (
	"net"
	"net/http"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/egress"
)

// NewTransport собирает транспорт по строке выхода из настроек провайдера.
//
// Разбор строки — в mybits/egress, своего здесь нет: схемы, ошибки и запрет
// молча уйти напрямую при опечатке должны быть одни на всех. Пустая строка —
// прямой выход. SOCKS5 живёт в DialContext (http.Transport через Proxy его не
// умеет), HTTP(S)-прокси — в Proxy.
//
// Транспорт возвращается готовым к передаче в http.Client. Ошибка значит, что
// строку выхода не разобрать, и тогда запрос лучше не начинать: с молча неверным
// прокси человек получил бы «нет связи» вместо «в адресе прокси опечатка».
func NewTransport(proxy string) (*http.Transport, error) {
	exit, err := egress.Parse(proxy)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 exit.ProxyFunc(),
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	switch exit.Scheme() {
	case "socks5", "socks5h":
		// Адрес назначения отдаём посреднику именем, а не разрешаем у себя:
		// иначе DNS-запрос уходил бы мимо прокси, и в сети, где он закрыт,
		// ничего бы не работало. Переменные среды здесь не читаем: выбран
		// socks5 — значит через socks5, а не через чужой HTTP_PROXY.
		transport.Proxy = nil
		transport.DialContext = exit.DialContext
	}
	return transport, nil
}
