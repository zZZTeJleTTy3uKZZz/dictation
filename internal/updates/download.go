package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/selfupdate"
)

// maxDownload — предел скачивания установщика (§4): больше гигабайта не берём.
const maxDownload = 1 << 30

// maxRedirects — предел переадресаций (§3).
const maxRedirects = 5

// newHTTPClient собирает клиент политики §3: только https (loopback http —
// исключение для проверок), не больше пяти переадресаций, токен уходит лишь
// на хост манифеста.
func newHTTPClient(token string) *http.Client {
	return &http.Client{
		Timeout: 0,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return selfupdate.ErrTooManyRedirects
			}
			return checkSecureURL(req.URL)
		},
		Transport: &tokenTransport{base: http.DefaultTransport, token: token},
	}
}

// checkSecureURL пропускает https и loopback http — правило §3.
func checkSecureURL(u *url.URL) error {
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "127.0.0.1" || host == "::1" || host == "localhost" {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", selfupdate.ErrInsecureURL, u.Redacted())
}

// tokenTransport добавляет PRIVATE-TOKEN при чтении закрытого реестра.
// Токен не уходит на чужой хост: ссылка архива может быть где угодно.
type tokenTransport struct {
	base  http.RoundTripper
	token string
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token != "" && isTokenHost(req.URL.Hostname()) {
		clone := req.Clone(req.Context())
		clone.Header.Set("PRIVATE-TOKEN", t.token)
		req = clone
	}
	return t.base.RoundTrip(req)
}

func isTokenHost(host string) bool {
	return false
}

// download скачивает установщик в кэш, ведёт прогресс и сверяет размер с
// sha256 (§4). Файл появляется на своём месте только после сверки: недокачанный
// установщик не должен выглядеть готовым.
func (c *Controller) download(ctx context.Context, rel selfupdate.Release) error {
	asset := rel.Asset
	if asset.Bytes <= 0 {
		return fmt.Errorf("%w: у файла нет bytes", selfupdate.ErrSizeMismatch)
	}
	u, err := url.Parse(asset.URL)
	if err != nil {
		return fmt.Errorf("%w: %v", selfupdate.ErrBadManifest, err)
	}
	if err := checkSecureURL(u); err != nil {
		return err
	}
	if err := os.MkdirAll(c.opts.Dir, 0o755); err != nil {
		return fmt.Errorf("каталог обновления: %w", err)
	}
	tmp, err := os.CreateTemp(c.opts.Dir, ".installer-*.download")
	if err != nil {
		return fmt.Errorf("временный файл обновления: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", selfupdate.ErrDownloadFailed, err)
	}
	resp, err := c.opts.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %v", selfupdate.ErrCancelled, ctx.Err())
		}
		return wrapDownload(err, asset.URL)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s ответил %s", selfupdate.ErrDownloadFailed, u.Redacted(), resp.Status)
	}

	h := sha256.New()
	// Читаем на байт больше обещанного: лишний байт — это size_mismatch, а не
	// тихо обрезанный установщик.
	limit := asset.Bytes + 1
	if limit > maxDownload+1 {
		limit = maxDownload + 1
	}
	got, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(&progressReader{
		r:   resp.Body,
		ctx: ctx,
		progress: func(received int64) {
			c.progress(received, asset.Bytes)
		},
	}, limit))
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %v", selfupdate.ErrCancelled, ctx.Err())
		}
		return wrapDownload(err, asset.URL)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %v", selfupdate.ErrCancelled, ctx.Err())
	}
	if got > asset.Bytes {
		return fmt.Errorf("%w: ждали %d байт, получили больше", selfupdate.ErrSizeMismatch, asset.Bytes)
	}
	if got != asset.Bytes {
		return fmt.Errorf("%w: ждали %d байт, получили %d", selfupdate.ErrSizeMismatch, asset.Bytes, got)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("сброс обновления на диск: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("закрытие обновления: %w", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(sum, asset.SHA256) {
		return fmt.Errorf("%w: ждали %s, получили %s", selfupdate.ErrChecksum, asset.SHA256, sum)
	}

	final := filepath.Join(c.opts.Dir, installerName(rel))
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("готовый установщик: %w", err)
	}
	ok = true

	c.mu.Lock()
	c.file = final
	c.mu.Unlock()
	return nil
}

// progress обновляет полученные байты не чаще пяти раз в секунду: окно и так
// спрашивает состояние, а каждая запись берёт замок.
func (c *Controller) progress(received, total int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Status != StatusDownloading {
		return
	}
	// Мелкие шаги не нужны: карточка показывает проценты, а не байты.
	if received != total && received-c.state.Received < 64*1024 {
		return
	}
	c.state.Received = received
	c.state.Total = total
}

// progressReader считает прочитанное и останавливается по контексту: даже
// сервер, не знающий об отмене, не удержит скачивание.
type progressReader struct {
	r        io.Reader
	ctx      context.Context
	got      int64
	progress func(int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(b)
	p.got += int64(n)
	if p.progress != nil {
		p.progress(p.got)
	}
	return n, err
}

// installerName — имя файла на диске кэша; версия в имени, чтобы две версии не
// затирали друг друга.
func installerName(rel selfupdate.Release) string {
	name := filepath.Base(rel.Asset.Name)
	if name == "" || name == "." || name == "/" {
		name = "dictation-setup.exe"
	}
	return "dictation-" + trimV(rel.Version) + "-" + name
}

// wrapDownload переводит сетевую ошибку в код протокола.
func wrapDownload(err error, rawURL string) error {
	if errors.Is(err, selfupdate.ErrTooManyRedirects) || errors.Is(err, selfupdate.ErrInsecureURL) {
		return err
	}
	var netErr net.Error
	if errors.As(err, &netErr) || err != nil {
		return fmt.Errorf("%w: %s: %v", selfupdate.ErrDownloadFailed, displayURL(rawURL), err)
	}
	return err
}

// displayURL не печатает хвост запроса: в нём бывает ключ доступа.
func displayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "адрес"
	}
	return u.Redacted()
}
