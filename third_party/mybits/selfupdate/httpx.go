package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// apiLimit — предел тела ответа API и манифеста. Список выпусков — килобайты;
// всё, что больше, — не тот сервер.
const apiLimit = 4 << 20

// tokenHeader — заголовок личного токена реестра.
const tokenHeader = "PRIVATE-TOKEN"

// fetchPolicy — правила сети источника (§3): сколько переадресаций и только
// ли https.
type fetchPolicy struct {
	// maxRedirects — предел цепочки; 0 — без своего предела.
	maxRedirects int
	// httpsOnly — каждая переадресация обязана вести на https (loopback http
	// допустим только в проверках).
	httpsOnly bool
}

// fetcher скачивает по ссылкам, прикладывая токен ТОЛЬКО к адресам своего
// источника (origin). Ссылка выпуска может вести куда угодно, а перенаправление
// — на хранилище объектов; токен, ушедший туда, считается утёкшим.
type fetcher struct {
	client *http.Client
	origin *url.URL // nil — токена нет вовсе
	token  string
}

func newFetcher(client *http.Client, origin *url.URL, token string) fetcher {
	return newFetcherPolicy(client, origin, token, fetchPolicy{maxRedirects: 10})
}

func newFetcherPolicy(client *http.Client, origin *url.URL, token string, pol fetchPolicy) fetcher {
	if client == nil {
		client = http.DefaultClient
	}
	// Копия клиента: свой CheckRedirect не должен просочиться в клиент
	// вызывающего. Токен в перенаправлении снимается, если хост сменился:
	// стандартная библиотека так делает только для Authorization и Cookie.
	c := *client
	orig := client.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if pol.httpsOnly {
			if err := checkSecureURL(req.URL); err != nil {
				return err
			}
		}
		if origin == nil || !sameOrigin(origin, req.URL) {
			req.Header.Del(tokenHeader)
		}
		if pol.maxRedirects > 0 && len(via) > pol.maxRedirects {
			return fmt.Errorf("%w: больше %d", ErrTooManyRedirects, pol.maxRedirects)
		}
		if orig != nil {
			return orig(req, via)
		}
		return nil
	}
	return fetcher{client: &c, origin: origin, token: token}
}

// sameOrigin — совпадают схема и хост с портом. Схема важна: токен не должен
// уходить открытым текстом на тот же хост по http.
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// open выполняет GET и отдаёт тело; не 2xx — ошибка с кодом.
func (f fetcher) open(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	rc, _, err := f.get(ctx, rawURL)
	return rc, err
}

// get — open вместе с итоговым адресом после переадресаций: от него
// разрешаются относительные url манифеста (§3).
func (f fetcher) get(ctx context.Context, rawURL string) (io.ReadCloser, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, codef(CodeDownloadFailed, ErrDownloadFailed, "запрос %s: %v", displayURL(rawURL), err)
	}
	req.Header.Set("User-Agent", "mybits-selfupdate")
	if f.token != "" && f.origin != nil && sameOrigin(f.origin, req.URL) {
		req.Header.Set(tokenHeader, f.token)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, withCode(CodeCancelled, fmt.Errorf("%w: %w", ErrCancelled, cerr))
		}
		return nil, nil, withCode(CodeDownloadFailed, fmt.Errorf("selfupdate: GET %s: %w", displayURL(rawURL), err))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, nil, codef(CodeDownloadFailed, ErrDownloadFailed, "GET %s: статус %d", displayURL(rawURL), resp.StatusCode)
	}
	final := req.URL
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL
	}
	return resp.Body, final, nil
}

// readAll читает тело целиком, но не больше limit байт (иначе ErrTooLarge).
func (f fetcher) readAll(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	data, _, err := f.readAllAt(ctx, rawURL, limit)
	return data, err
}

// readAllAt — readAll вместе с итоговым адресом ответа.
func (f fetcher) readAllAt(ctx context.Context, rawURL string, limit int64) ([]byte, *url.URL, error) {
	rc, final, err := f.get(ctx, rawURL)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, withCode(CodeCancelled, fmt.Errorf("%w: %w", ErrCancelled, cerr))
		}
		return nil, nil, withCode(CodeDownloadFailed, fmt.Errorf("selfupdate: чтение %s: %w", displayURL(rawURL), err))
	}
	if int64(len(data)) > limit {
		return nil, nil, codef(CodeTooLarge, ErrTooLarge, "ответ %s больше %d байт", displayURL(rawURL), limit)
	}
	return data, final, nil
}

// displayURL — адрес для сообщений об ошибках: без запроса и без учётных
// данных (в подписанных ссылках там бывают секреты).
func displayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<адрес>"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
