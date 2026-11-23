// Прокси для запросов распознавания — на общем ядре speech/conn.
//
// ЗАЧЕМ. Из некоторых сетей api.groq.com не отвечает вовсе: 403 без единого
// слова, обрыв на рукопожатии TLS или тишина до таймаута. Виновата не программа,
// и «проверьте интернет» тут не помогает — помогает посредник, через который
// запрос доходит. Человек вписывает адрес прокси, и диктовка ходит через него.
//
// ЧТО ПОДДЕРЖИВАЕТСЯ. HTTP(S) и SOCKS5 — то, что дают обычные клиенты вроде
// Hiddify, v2ray и прокси-клиентов роутеров. Логин и пароль необязательны.
//
// ЧТО ЗДЕСЬ ОСТАЛОСЬ. Своего SOCKS5 больше нет (был socks5.go на двести строк):
// строку настройки разбирает mybits/egress, транспорт собирает conn.NewTransport,
// и та же проверка стоит при сохранении записи провайдера. Здесь только форма
// настройки Диктовки — три поля, которые окно показывает отдельно, — и перевод
// их в строку выхода ядра.
//
// ЧЕГО ЗДЕСЬ НЕТ. Своего списка адресов и «умного» выбора прокси: адрес один и
// тот, что вписал человек. Угадывать за него, через что ходить, — значит однажды
// отправить звук и ключ туда, куда он не собирался.
package providercfg

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/speech/conn"
	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/egress"
)

// Proxy — настройка посредника так, как её видит окно.
type Proxy struct {
	// URL — адрес вида http://127.0.0.1:8080 или socks5://127.0.0.1:1080.
	// Имя и пароль в адресе не пишутся: для них отдельные поля.
	URL string
	// User и Password — если посредник их требует.
	User     string
	Password string
}

// Empty — прокси не задан: запросы идут прямо.
func (p Proxy) Empty() bool { return strings.TrimSpace(p.URL) == "" }

// Exit — строка выхода для ядра: адрес, а имя и пароль — внутри адреса.
//
// Пусто — и у пустого прокси, и у адреса, который не разобрать: разбираемость
// проверяет ValidateProxy, а сюда попадает то, что уже принято.
func (p Proxy) Exit() string {
	address := strings.TrimSpace(p.URL)
	if address == "" {
		return ""
	}
	if p.User == "" && p.Password == "" {
		return address
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return address
	}
	parsed.User = url.UserPassword(p.User, p.Password)
	return parsed.String()
}

// Describe — как прокси называется для человека. Пароль сюда не попадает
// нарочно: строку показывают в окне и пишут в журнал.
func (p Proxy) Describe() string {
	if p.Empty() {
		return ""
	}
	parsed, err := parseProxyURL(p)
	if err != nil {
		return strings.TrimSpace(p.URL)
	}
	return parsed.Scheme + "://" + parsed.Host
}

// parseProxyURL — разобрать и проверить адрес посредника.
//
// Разбор и сообщения об опечатках — ядра: «сохранилось» должно значить
// «поднимется», и проверка при сохранении не может знать меньше, чем транспорт.
func parseProxyURL(p Proxy) (*url.URL, error) {
	exit, err := egress.Parse(p.Exit())
	if err != nil {
		// Поля окна отдельные, а ядру нужна одна строка: пустой адрес — просьба
		// вписать его, а не «схема не указана».
		if strings.TrimSpace(p.URL) == "" {
			return nil, errors.New("не задан адрес прокси: например socks5://127.0.0.1:1080")
		}
		return nil, explainProxy(err)
	}
	return url.Parse(exit.String())
}

// explainProxy — ошибка ядра словами Диктовки.
//
// Разбор один (mybits/egress), и это нарочно: «сохранилось» должно значить
// «поднимется». Здесь только обёртка, чтобы в журнале было видно, что дело в
// прокси, а не в адресе сервиса.
func explainProxy(err error) error {
	var notUnderstood egress.NotUnderstoodError
	if errors.As(err, &notUnderstood) {
		return notUnderstood
	}
	return fmt.Errorf("адрес прокси: %w", err)
}

// NewClient — клиент, который ходит через прокси.
//
// Пустая настройка — обычный клиент. Ошибка значит, что адрес не разобрать, и
// тогда запрос лучше не начинать: с молча неверным прокси человек получил бы
// «нет связи» вместо «в адресе прокси опечатка».
func NewClient(p Proxy, timeout time.Duration) (*http.Client, error) {
	// Ноль значит «без общего срока»: у распознавания срок свой, и он длиннее
	// любого разумного общего — запись в полчаса разбирается минутами.
	if p.Empty() {
		return &http.Client{Timeout: timeout}, nil
	}
	transport, err := conn.NewTransport(p.Exit())
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// ValidateProxy — проверить адрес посредника, ничего не начиная.
//
// Нужен тем, кто сохраняет настройку: опечатку в адресе человек должен увидеть
// сразу, а не при первой диктовке, когда «нет связи» ничего не объясняет.
func ValidateProxy(p Proxy) error {
	if p.Empty() {
		return nil
	}
	_, err := parseProxyURL(p)
	return err
}
