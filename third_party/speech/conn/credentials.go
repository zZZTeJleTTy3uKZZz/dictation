package conn

import (
	"context"
	"errors"
)

// ErrNotImplemented — ещё не сделано. Так честнее заглушки, которая делает вид,
// что сработала: вызывающий обязан показать человеку «вход через Цифро ID
// скоро», а не «ключ работает».
var ErrNotImplemented = errors.New("вход через Цифро ID пока не реализован")

// StaticKey — готовый ключ, который человек вписал сам (или получил ссылкой).
// Самый простой способ доступа: ключ уже есть, ничего получать не надо.
type StaticKey string

// Token отдаёт ключ как есть.
func (k StaticKey) Token(context.Context) (string, error) {
	if k == "" {
		return "", ErrSecretNotFound
	}
	return string(k), nil
}

// CredentialSource — откуда берётся ключ провайдера.
//
// Сегодня это StaticKey; вход через Цифро ID (DeviceFlow) описан тем же
// интерфейсом, чтобы окну и CLI не пришлось потом узнавать второй способ.
type CredentialSource interface {
	// Token отдаёт ключ для запроса или ошибку (нет ключа, вход не завершён).
	Token(ctx context.Context) (string, error)
}

// DeviceFlow — вход по RFC 8628 (device authorization grant) для провайдеров
// Цифро ID.
//
// Интерфейс заведён сейчас, реализация — отдельной задачей: сам поток (запрос
// кода устройства, ожидание подтверждения, обновление токена) зависит от
// сервера Цифро ID, а обрывками он не собирается.
type DeviceFlow interface {
	// Begin просит у сервера код устройства и адрес подтверждения, который
	// человек открывает в браузере.
	Begin(ctx context.Context) (DeviceAuthorization, error)
	// Poll ждёт, пока человек подтвердит вход, и отдаёт токен доступа.
	Poll(ctx context.Context, authorization DeviceAuthorization) (Token, error)
}

// DeviceAuthorization — ответ сервера на запрос кода устройства (RFC 8628 §3.2).
type DeviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval,omitempty"`
}

// Token — выданный токен.
type Token struct {
	AccessToken  string
	RefreshToken string
	// ExpiresIn — срок в секундах; 0 — сервер срок не назвал.
	ExpiresIn int
}

// deviceFlow — заглушка входа через Цифро ID.
type deviceFlow struct{}

// NewDeviceFlow — вход через Цифро ID. Пока заглушка: обе операции отказывают
// ErrNotImplemented, и вызывающий показывает это человеку.
func NewDeviceFlow() DeviceFlow { return deviceFlow{} }

func (deviceFlow) Begin(context.Context) (DeviceAuthorization, error) {
	return DeviceAuthorization{}, ErrNotImplemented
}

func (deviceFlow) Poll(context.Context, DeviceAuthorization) (Token, error) {
	return Token{}, ErrNotImplemented
}
