// Package egress — выход наружу: строка настройки превращается в соединение.
//
// # Зачем
//
// Служба ходит к чужим серверам не всегда прямо: у каждого аккаунта бывает
// свой адрес выхода, потому что чужая сторона смотрит не на страну саму по
// себе, а на СМЕНУ привычного захода. Адрес приходит строкой из настроек
// («socks5://имя:пароль@узел:1080»), а нужен из него `net.Conn`.
//
// # Список схем закрытый
//
// Схема, которой здесь нет, — отказ, а не «пусть библиотека разберётся».
// Разбираться библиотеке пришлось бы ровно в тот момент, когда человек
// ошибся в строке: молча уйти прямым выходом на опечатке значит потерять
// защиту тогда, когда она и нужна. Умеем четыре схемы, и каждая за собой
// что-то имеет — записанной «поддержки», за которой нет кода, тут нет.
//
// # Отказ вместо тихого обхода
//
// Прокси не отвечает — соединение не состоялось, и ошибка говорит про выход
// (DialError). Уйти напрямую «раз уж не вышло» нельзя: тот, кто задал адрес,
// считает, что защищён, и узнает правду не от нас, а от чужой стороны.
//
// # Пароль не попадает в журналы
//
// В строке выхода живёт пароль. Поэтому String отдаёт адрес с затёртым
// паролем, а сама структура ничего не печатает целиком: строка, которую
// удобно подставить в журнал, обязана быть безопасной по умолчанию, иначе
// секрет утечёт первой же отладочной записью.
package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// Schemes — схемы выхода, которые пакет умеет.
var Schemes = []string{"socks5", "socks5h", "http", "https"}

// NotUnderstoodError — адрес записан так, что по нему нельзя подключиться.
//
// Отдельным типом, а не текстом: эту беду показывают человеку в кабинете в
// момент ввода, и отличать её от «прокси не ответил» приходится вызывающему.
type NotUnderstoodError struct{ Message string }

func (e NotUnderstoodError) Error() string { return e.Message }

// DialError — выход задан, разобран, но соединиться через него не вышло.
//
// Несёт безопасную строку выхода, чтобы отказ называл виновника: без неё
// оператор видит «таймаут» и ищет поломку в мессенджере, а не в прокси.
type DialError struct {
	Egress string
	Err    error
}

func (e DialError) Error() string {
	return fmt.Sprintf("выход %s не сработал: %v", e.Egress, e.Err)
}

func (e DialError) Unwrap() error { return e.Err }

// Egress — разобранный адрес выхода. Нулевое значение — прямой выход.
type Egress struct {
	addr *url.URL
	raw  string
}

// Parse разбирает строку выхода.
//
// Пустая строка — законный прямой выход, а не «настройка забыта»: у своего
// сервера в нужной стране адрес и так правильный, и лишний узел добавил бы
// только точку отказа.
func Parse(raw string) (Egress, error) {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return Egress{}, nil
	}
	addr, err := url.Parse(clean)
	if err != nil {
		// Отдельной жалобой, а не «схема неизвестна»: чаще всего сюда
		// приводит логин или пароль не латиницей либо со знаком «@» внутри —
		// и совет «укажите схему» в этом случае уводит человека не туда.
		// Текста самой ошибки здесь нет: он несёт кусок адреса, то есть
		// пароль.
		return Egress{}, NotUnderstoodError{Message: fmt.Sprintf(
			"Выход «%s»: адрес не разбирается. Имя и пароль записываются "+
				"латиницей, а особые знаки в них — кодами (%%40 вместо @).",
			safe(clean))}
	}
	if !knownScheme(addr.Scheme) {
		scheme := "не указана"
		if addr.Scheme != "" {
			scheme = addr.Scheme
		}
		return Egress{}, NotUnderstoodError{Message: fmt.Sprintf(
			"Выход «%s»: схема %s нам неизвестна. Умеем: %s. Без схемы адрес "+
				"нельзя отличить от прямого выхода.",
			safe(clean), scheme, strings.Join(Schemes, ", "))}
	}
	if addr.Hostname() == "" {
		return Egress{}, NotUnderstoodError{Message: fmt.Sprintf(
			"Выход «%s»: не разобрать узел, куда идти", safe(clean))}
	}
	if addr.Port() == "" {
		// Порт не угадываем: у socks5 он 1080, у http-прокси 3128 или 8080, и
		// выбранное за человека умолчание уводит соединение не туда молча.
		return Egress{}, NotUnderstoodError{Message: fmt.Sprintf(
			"Выход «%s»: не указан порт. Угадывать его нельзя — у каждого "+
				"прокси он свой.", safe(clean))}
	}
	return Egress{addr: addr, raw: clean}, nil
}

// Address — адрес выхода целиком, С ПАРОЛЕМ, очищенный от пробелов по краям.
//
// Нужен ровно одному: тому, кто адрес ХРАНИТ. Всё остальное — журналы,
// отказы, кабинет — берёт String, где пароль затёрт. Метод назван так, чтобы
// его нельзя было позвать случайно вместо печати.
func (e Egress) Address() string { return e.raw }

// Direct говорит, что выход не задан и идти надо прямо с машины.
func (e Egress) Direct() bool { return e.addr == nil }

// Scheme — схема выхода («socks5»), либо пусто у прямого.
func (e Egress) Scheme() string {
	if e.addr == nil {
		return ""
	}
	return e.addr.Scheme
}

// String — адрес выхода С ЗАТЁРТЫМ ПАРОЛЕМ, годный для журнала и для отказа
// человеку. Другого способа напечатать выход у пакета нет намеренно.
func (e Egress) String() string {
	if e.addr == nil {
		return "прямой"
	}
	return e.addr.Redacted()
}

// ProxyFunc отдаёт выход в виде, который понимает http.Transport.
//
// Нужен там, где соединение делает не наш код, а чужая библиотека, умеющая
// только `Proxy func(*http.Request) (*url.URL, error)`.
func (e Egress) ProxyFunc() func(*http.Request) (*url.URL, error) {
	if e.addr == nil {
		return nil
	}
	if e.Scheme() == "http" || e.Scheme() == "https" {
		return http.ProxyURL(e.addr)
	}
	// SOCKS через Proxy у http.Transport не ходит — там его место занимает
	// DialContext. Возвращаем пусто, чтобы вызывающий взял DialContext, а не
	// думал, что прокси применён.
	return nil
}

// DialContext соединяется с адресом через выход.
//
// Подпись — та же, что у net.Dialer.DialContext, чтобы функцию можно было
// отдать любой библиотеке, которая просит «чем соединяться».
func (e Egress) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	direct := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if e.addr == nil {
		return direct.DialContext(ctx, network, address)
	}
	conn, err := e.dialThrough(ctx, direct, network, address)
	if err != nil {
		// Заворачиваем ВСЕГДА: без этого отказ выглядит как поломка чужой
		// стороны, и чинить идут не туда.
		return nil, DialError{Egress: e.String(), Err: err}
	}
	return conn, nil
}

func (e Egress) dialThrough(ctx context.Context, direct *net.Dialer, network, address string) (net.Conn, error) {
	switch e.Scheme() {
	case "socks5", "socks5h":
		// socks5h отличается от socks5 только тем, что имя разрешает прокси, а
		// не мы. Библиотека так и делает всегда, поэтому обе схемы — один код.
		dialer, err := proxy.SOCKS5("tcp", e.addr.Host, socksAuth(e.addr), direct)
		if err != nil {
			return nil, err
		}
		ctxDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("выход socks5 не умеет отмену — библиотека изменилась")
		}
		return ctxDialer.DialContext(ctx, network, address)
	case "http", "https":
		return e.dialConnect(ctx, direct, network, address)
	}
	// Сюда попасть нельзя: схемы проверены при разборе. Но если однажды
	// список разойдётся с этим переключателем, пусть будет отказ, а не
	// прямой выход украдкой.
	return nil, fmt.Errorf("схема выхода %s не умеет соединяться", e.Scheme())
}

// dialConnect делает CONNECT на HTTP-прокси.
//
// Руками, а не через http.Transport: транспорт отдаёт готовый ответ, а нам
// нужен сам сокет, по которому дальше пойдёт не HTTP, а websocket или
// MTProto.
func (e Egress) dialConnect(ctx context.Context, direct *net.Dialer, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("через HTTP-прокси ходит только tcp, просят %s", network)
	}
	conn, err := direct.DialContext(ctx, "tcp", e.addr.Host)
	if err != nil {
		return nil, err
	}
	if e.Scheme() == "https" {
		conn = tls.Client(conn, &tls.Config{ServerName: e.addr.Hostname()})
		if handshake, ok := conn.(*tls.Conn); ok {
			if err := handshake.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				return nil, err
			}
		}
	}
	// Срок разговора с прокси берём из контекста: без него зависший прокси
	// держит подъём канала бесконечно, а мы этого не видим.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: address},
		Host:   address,
		Header: make(http.Header),
	}
	if user := e.addr.User; user != nil {
		password, _ := user.Password()
		request.Header.Set("Proxy-Authorization", "Basic "+basic(user.Username(), password))
	}
	if err := request.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("прокси ответил на CONNECT: %s", response.Status)
	}
	if reader.Buffered() > 0 {
		// Прокси прислал данные вперёд нас — по CONNECT так не делают, и
		// молча их потерять значит отдать наверх сокет, с которого уже
		// пропал кусок разговора.
		_ = conn.Close()
		return nil, errors.New("прокси прислал данные до конца CONNECT")
	}
	// Снимаем общий срок: дальше по этому сокету идёт долгий разговор
	// (websocket живёт часами), и унаследованный срок оборвал бы его.
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func socksAuth(addr *url.URL) *proxy.Auth {
	if addr.User == nil {
		return nil
	}
	password, _ := addr.User.Password()
	return &proxy.Auth{User: addr.User.Username(), Password: password}
}

func knownScheme(scheme string) bool {
	for _, known := range Schemes {
		if scheme == known {
			return true
		}
	}
	return false
}

// safe затирает пароль в строке, которую не удалось разобрать целиком.
//
// Жалоба на кривой адрес — это тоже журнал: строку «socks5://имя:пароль@»
// без порта человек увидит в отказе, и пароль уйдёт туда же.
func safe(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	scheme := strings.Index(raw, "://")
	if scheme < 0 {
		return "***" + raw[at:]
	}
	return raw[:scheme+3] + "***" + raw[at:]
}

func basic(user, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
}
