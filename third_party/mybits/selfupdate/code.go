package selfupdate

import (
	"context"
	"errors"
	"fmt"
)

// Дополнительные ошибки протокола update/1 (§6). Прежние Err* остаются
// как были: коды ниже ложатся на них, и errors.Is(err, ErrChecksum) продолжает
// работать для checksum_mismatch.
var (
	// ErrBadManifest — документ или файл не соответствует протоколу.
	ErrBadManifest = errors.New("selfupdate: неверный манифест")
	// ErrInsecureURL — адрес не https (loopback http допустим только в проверках).
	ErrInsecureURL = errors.New("selfupdate: небезопасный адрес, нужен https")
	// ErrSizeMismatch — скачано не столько байт, сколько обещал манифест.
	ErrSizeMismatch = errors.New("selfupdate: размер файла не совпал с манифестом")
	// ErrDownloadFailed — загрузка не удалась (сеть, статус, обрыв).
	ErrDownloadFailed = errors.New("selfupdate: скачивание не удалось")
	// ErrInstallFailed — установщик не отработал.
	ErrInstallFailed = errors.New("selfupdate: установка не удалась")
	// ErrCancelled — работу прервал контекст.
	ErrCancelled = errors.New("selfupdate: отменено")
	// ErrUnsupported — вид установки на этой платформе не поддерживается.
	ErrUnsupported = errors.New("selfupdate: не поддерживается на этой платформе")
	// ErrTooManyRedirects — цепочка переадресаций длиннее допустимой.
	ErrTooManyRedirects = errors.New("selfupdate: слишком много перенаправлений")
)

// Code — код ошибки протокола (§6). Один и тот же в Go, Dart и карточке
// обновления. Пустая строка — ошибки протокола нет.
type Code string

// Коды ошибок §6.
const (
	CodeBadManifest      Code = "bad_manifest"
	CodeBadVersion       Code = "bad_version"
	CodeInsecureURL      Code = "insecure_url"
	CodeNoAsset          Code = "no_asset"
	CodeNoChecksum       Code = "no_checksum"
	CodeChecksumMismatch Code = "checksum_mismatch"
	CodeSizeMismatch     Code = "size_mismatch"
	CodeTooLarge         Code = "too_large"
	CodeDownloadFailed   Code = "download_failed"
	CodeInstallFailed    Code = "install_failed"
	CodeCancelled        Code = "cancelled"
)

// Retryable говорит, имеет ли смысл предлагать «Повторить» (§6).
func (c Code) Retryable() bool {
	switch c {
	case CodeDownloadFailed, CodeChecksumMismatch, CodeSizeMismatch, CodeInstallFailed:
		return true
	}
	return false
}

// Err возвращает прежнюю ошибку-значение, на которую ложится код; nil — у
// кода своей ошибки нет.
func (c Code) Err() error {
	switch c {
	case CodeBadManifest:
		return ErrBadManifest
	case CodeBadVersion:
		return ErrBadVersion
	case CodeInsecureURL:
		return ErrInsecureURL
	case CodeNoAsset:
		return ErrNoAsset
	case CodeNoChecksum:
		return ErrNoChecksum
	case CodeChecksumMismatch:
		return ErrChecksum
	case CodeSizeMismatch:
		return ErrSizeMismatch
	case CodeTooLarge:
		return ErrTooLarge
	case CodeDownloadFailed:
		return ErrDownloadFailed
	case CodeInstallFailed:
		return ErrInstallFailed
	case CodeCancelled:
		return ErrCancelled
	}
	return nil
}

// codedError — ошибка с кодом протокола; разворачивается в Err*, поэтому
// errors.Is(err, ErrChecksum) и CodeOf(err) работают вместе.
type codedError struct {
	code Code
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }

func (e *codedError) Unwrap() error { return e.err }

// Code возвращает код протокола (для CodeOf).
func (e *codedError) Code() Code { return e.code }

// withCode приписывает ошибке код протокола. nil остаётся nil.
func withCode(code Code, err error) error {
	if err == nil {
		return nil
	}
	return &codedError{code: code, err: err}
}

// codef собирает ошибку «сентинел + текст» и приписывает ей код.
func codef(code Code, sentinel error, format string, args ...any) error {
	return withCode(code, fmt.Errorf("%w: "+format, append([]any{sentinel}, args...)...))
}

// CodeOf возвращает код протокола у ошибки (§6). Для старых путей, которые
// ещё не приписывают код явно, он выводится из сентинела Err*.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var ce interface{ Code() Code }
	if errors.As(err, &ce) {
		if c := ce.Code(); c != "" {
			return c
		}
	}
	switch {
	case errors.Is(err, ErrCancelled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return CodeCancelled
	case errors.Is(err, ErrBadVersion):
		return CodeBadVersion
	case errors.Is(err, ErrInsecureURL):
		return CodeInsecureURL
	case errors.Is(err, ErrNoAsset):
		return CodeNoAsset
	case errors.Is(err, ErrNoChecksum):
		return CodeNoChecksum
	case errors.Is(err, ErrChecksum):
		return CodeChecksumMismatch
	case errors.Is(err, ErrSizeMismatch):
		return CodeSizeMismatch
	case errors.Is(err, ErrTooLarge):
		return CodeTooLarge
	case errors.Is(err, ErrInstallFailed), errors.Is(err, ErrUnsupported):
		return CodeInstallFailed
	case errors.Is(err, ErrDownloadFailed), errors.Is(err, ErrTooManyRedirects):
		return CodeDownloadFailed
	case errors.Is(err, ErrBadManifest), errors.Is(err, ErrUnsafeArchive), errors.Is(err, ErrBinaryNotFound):
		return CodeBadManifest
	}
	return ""
}
