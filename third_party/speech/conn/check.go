package conn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/speech"
)

// checkTimeout — сколько ждём ответа на проверку. Настоящая запись разбирается
// минутами, а «ты здесь?» должен отвечать за секунды.
const checkTimeout = 20 * time.Second

// Check — настоящий пробный запрос к провайдеру.
//
// «Ключ сохранён» ничего не говорит: он может быть отозван или не принят из
// этой сети. Проверка отправляет короткий встроенный образец на
// /audio/transcriptions — ровно тот путь, которым живёт диктовка. Если он
// прошёл, пройдёт и настоящая запись; список моделей для этого не нужен
// (у OpenAI-совместимых сервисов его бывает нет, и «нет такого пути» ещё не
// значит «ключ плохой»).
//
// Ключ берётся из creds: сегодня это StaticKey (человек вписал ключ сам или
// получил ссылкой), позже — вход через Цифро ID. Поэтому сигнатура не «key
// string»: когда появится DeviceFlow, Check не придётся менять, а вызывающий
// подставит свой источник. Нет ключа (ErrSecretNotFound) или вход не завершён
// (ErrNotImplemented) — отказ с кодом speech.CodeKeyRejected и советом, что
// сделать.
//
// Итог — словарь speech.Classify: окно, история и CLI объясняют один и тот же
// отказ одними словами. Ошибка возвращается, только когда проверку нельзя было
// начать: неверная запись провайдера или строка выхода.
func Check(ctx context.Context, provider Provider, creds CredentialSource) (speech.Verdict, error) {
	normalized, err := provider.Normalize()
	if err != nil {
		return speech.Verdict{}, err
	}
	if creds == nil {
		return speech.Verdict{}, errors.New("не указан источник ключа: нужен StaticKey или вход через Цифро ID")
	}
	token, err := creds.Token(ctx)
	if err != nil {
		// Отмена — решение вызывающего, а не отказ: отдаём как есть.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return speech.Verdict{}, ctxErr
		}
		// Совет наш, а не текст секретаря: у источника может быть своя форма
		// входа (Цифро ID), и пересказывать её словами «впишите ключ» нельзя.
		return speech.Classify(&speech.Error{Code: speech.CodeKeyRejected}, nil), nil
	}
	transport, err := NewTransport(normalized.Proxy)
	if err != nil {
		return speech.Verdict{}, fmt.Errorf("прокси: %w", err)
	}
	client, err := speech.New(speech.Config{
		BaseURL: normalized.BaseURL,
		Key:     strings.TrimSpace(token),
		Model:   normalized.Model,
		HTTP:    &http.Client{Transport: transport, Timeout: checkTimeout},
	})
	if err != nil {
		return speech.Verdict{}, err
	}
	ask, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	_, err = client.Transcribe(ask, speech.Request{
		Audio:    SampleWAV(),
		Filename: "check.wav",
	})
	if err == nil {
		return speech.Verdict{Reason: "сервис принял запрос — ключ работает"}, nil
	}
	if errors.Is(err, context.Canceled) {
		return speech.Verdict{}, context.Canceled
	}
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		return speech.Verdict{}, ctxErr
	}
	return speech.Classify(err, nil), nil
}
