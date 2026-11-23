package selfupdate

import (
	"context"
	"fmt"
	"net/http"
)

// StaticManifest — выпуск, описанный JSON-документом по адресу. Понимает
// форму update/1 (§2) и прежнюю mybits (§7):
//
//	{"protocol":"update/1","app":"tool","version":"1.2.3",
//	 "version_code":10203,"assets":[{"kind":"archive","os":"linux",
//	 "arch":"amd64","url":"tool_1.2.3_linux_amd64.tar.gz",
//	 "sha256":"…","bytes":1234}]}
//
// Относительные url разрешаются от итогового адреса документа (после
// переадресаций). Манифест и файлы — только https; http допустим лишь для
// loopback в проверках (§3). Подходит там, где нет внешнего реестра: статика на своём
// сервере. Токена у источника нет — файлы должны быть доступны по ссылкам
// как есть (подписанные ссылки подходят).
type StaticManifest struct {
	// URL манифеста.
	URL string
	// HTTPClient; nil — http.DefaultClient.
	HTTPClient *http.Client
}

// maxManifestRedirects — предел переадресаций манифеста (§3).
const maxManifestRedirects = 5

// Releases скачивает манифест и возвращает его единственный выпуск.
func (m StaticManifest) Releases(ctx context.Context) ([]Release, error) {
	if _, err := secureURL(m.URL); err != nil {
		return nil, fmt.Errorf("selfupdate: StaticManifest: %w", err)
	}
	f := newFetcherPolicy(m.HTTPClient, nil, "", fetchPolicy{
		maxRedirects: maxManifestRedirects,
		httpsOnly:    true,
	})
	body, final, err := f.readAllAt(ctx, m.URL, apiLimit)
	if err != nil {
		return nil, err
	}
	man, err := ParseManifest(body)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: StaticManifest: %w", err)
	}
	if err := man.Resolve(final); err != nil {
		return nil, fmt.Errorf("selfupdate: StaticManifest: %w", err)
	}
	rel := man.Release()
	rel.fetch = f.open
	return []Release{rel}, nil
}
