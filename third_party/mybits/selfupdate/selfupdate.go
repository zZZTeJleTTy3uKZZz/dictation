// Package selfupdate — самообновление программы из выпуска.
//
// Зачем он есть. У каждого нашего консольного инструмента (skillery, crew,
// atlas, line) своя копия «скачать новую сборку и подменить себя», и в
// каждой чего-то нет: суммы проверяют после распаковки, архив распаковывают
// без оглядки на «../», запущенный .exe на Windows заменить не умеют, а при
// сбое посреди замены остаётся наполовину записанный бинарник.
//
// Что делает пакет:
//
//   - Check спрашивает источник (Source) о выпусках, берёт наибольшую версию
//     по SemVer и выбирает файл под платформу;
//   - Apply скачивает файл рядом с программой, сверяет sha256 ДО распаковки,
//     безопасно достаёт бинарник и подменяет им запущенный: на Linux/macOS —
//     одним rename, на Windows — через «имя.exe.old»; при сбое любого шага
//     старый бинарник остаётся как был, временные файлы убираются;
//   - Install ставит файл по виду (kind) протокола update/1: binary и
//     archive — через Apply, installer — запуском тихого установщика Windows;
//   - CleanupOld убирает «имя.exe.old» при следующем запуске.
//
// Источники: ReleasesSource (выпуски внешнего реестра в формате ci-platform) и
// StaticManifest (JSON по адресу, форма update/1 и прежняя mybits).
//
// Чего пакет НЕ делает: не читает окружение (токен приходит полем), не
// запускает новую версию, не проверяет подпись выпуска — только sha256 из
// того же источника, что защищает от битой загрузки, но не от подмены самого
// источника. Зависимостей, кроме стандартной библиотеки, нет.
package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"
)

// Ошибки-значения: проверяются через errors.Is. Коды протокола §6 — Code,
// CodeOf.
var (
	// ErrNoAsset — в выпуске нет файла под эту платформу.
	ErrNoAsset = errors.New("selfupdate: в выпуске нет файла под эту платформу")
	// ErrNoChecksum — у файла нет известной суммы sha256: вслепую не качаем.
	ErrNoChecksum = errors.New("selfupdate: у файла нет суммы sha256")
	// ErrChecksum — скачанное не совпало с суммой выпуска.
	ErrChecksum = errors.New("selfupdate: сумма sha256 не совпала")
	// ErrUnsafeArchive — в архиве путь с «..», абсолютный или с обратной косой.
	ErrUnsafeArchive = errors.New("selfupdate: небезопасный путь в архиве")
	// ErrBinaryNotFound — в архиве нет ровно одного подходящего бинарника.
	ErrBinaryNotFound = errors.New("selfupdate: в архиве нет ровно одного нужного бинарника")
	// ErrBadVersion — строка не является версией SemVer.
	ErrBadVersion = errors.New("selfupdate: непонятная версия")
	// ErrTooLarge — ответ или распаковка больше допустимого предела.
	ErrTooLarge = errors.New("selfupdate: превышен предел размера")
)

// Asset — один скачиваемый файл выпуска под одну платформу.
type Asset struct {
	// OS и Arch в терминах GOOS/GOARCH: linux, windows, darwin, android;
	// amd64, arm64. Arch «any» подходит к любой (§2).
	OS, Arch string
	// Name — имя файла; по расширению Apply выбирает способ распаковки.
	Name string
	// URL — откуда скачивать.
	URL string
	// SHA256 — сумма файла в hex; пусто — Apply откажет (ErrNoChecksum).
	SHA256 string

	// Kind — вид файла update/1 (§2). Пусто — прежний выпуск реестра.
	Kind Kind
	// Bytes — точный размер файла; 0 — неизвестен (прежняя форма §7),
	// сверка размера пропускается.
	Bytes int64
	// Installer — вид установщика Windows для kind: installer.
	Installer InstallerKind
	// Args — свои параметры установщика вместо умолчания §5.
	Args []string
}

// Release — один выпуск.
type Release struct {
	// Version как в источнике, например "v1.4.0" или "1.4.0".
	Version    string
	Prerelease bool
	Notes      string
	Assets     []Asset
	// Asset — файл под платформу; заполняет Check.
	Asset Asset

	// Поля манифеста update/1 (§2). У ReleasesSource и прежней формы
	// mybits пусты или нулевые.
	Protocol       string
	App            string
	Channel        string
	VersionCode    int64
	MinVersionCode int64
	PublishedAt    time.Time
	// Decision — available, up_to_date или mandatory; заполняет Check.
	Decision Decision

	// fetch — как скачивать ссылки выпуска (с токеном источника); nil —
	// обычный http.
	fetch func(ctx context.Context, url string) (io.ReadCloser, error)
	// resolve — досбор данных выпуска, нужных только победителю (суммы
	// файлов): Check зовёт его после того, как выбрал выпуск, чтобы не
	// скачивать суммы двадцати выпусков ради одного.
	resolve func(ctx context.Context, r *Release) error
}

// Source — откуда берутся выпуски.
type Source interface {
	Releases(ctx context.Context) ([]Release, error)
}

// CheckOptions — настройки проверки.
type CheckOptions struct {
	// Prerelease допускает пред-выпуски (1.5.0-rc.1) как цель обновления.
	Prerelease bool
	// OS и Arch — целевая платформа; пусто — runtime.GOOS/GOARCH.
	OS, Arch string
	// Kinds — допустимые виды файлов в порядке предпочтения (§5): например
	// {"installer", "archive"}. Пусто — любой вид в порядке документа.
	Kinds []string
	// CurrentCode — version_code текущей версии, если известен (Android).
	// Вместе с version_code манифеста задаёт решение по коду, а не по SemVer.
	CurrentCode int64
	// App — своё имя приложения; пусто — app манифеста не сверяется.
	App string
}

// Check находит самый новый выпуск и говорит, новее ли он current.
//
// Возвращается самый новый подходящий выпуск и в случае «обновления нет»
// (false): им можно напечатать «у вас последняя версия». Asset выбран только
// когда обновление есть. Нечитаемая current («dev») — ErrBadVersion: что с
// ней делать, решает вызывающий. Выпуски с нечитаемой версией в источнике
// пропускаются.
//
// Решение об обновлении лежит в Release.Decision: available, up_to_date или
// mandatory (§5). Обязательным оно становится, когда известен
// opts.CurrentCode и он меньше min_version_code манифеста.
func Check(ctx context.Context, src Source, current string, opts CheckOptions) (Release, bool, error) {
	cur, curErr := parseSemver(current)
	rels, err := src.Releases(ctx)
	if err != nil {
		return Release{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return Release{}, false, err
	}

	var (
		best    Release
		bestVer semver
		found   bool
	)
	for _, r := range rels {
		v, err := parseSemver(r.Version)
		if err != nil {
			continue
		}
		if (r.Prerelease || len(v.pre) > 0) && !opts.Prerelease {
			continue
		}
		if !found || compareSemver(v, bestVer) > 0 {
			best, bestVer, found = r, v, true
		}
	}
	if !found {
		return Release{}, false, nil
	}
	if opts.App != "" && best.App != "" && best.App != opts.App {
		return Release{}, false, codef(CodeBadManifest, ErrBadManifest, "манифест приложения %q, а ждали %q", best.App, opts.App)
	}
	newer, err := newerThan(cur, curErr, current, opts.CurrentCode, best)
	if err != nil {
		return Release{}, false, err
	}
	if !newer {
		best.Decision = DecisionUpToDate
		return best, false, nil
	}

	if best.resolve != nil {
		if err := best.resolve(ctx, &best); err != nil {
			return Release{}, false, err
		}
	}
	goos, goarch := opts.OS, opts.Arch
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	asset, err := selectAsset(best.Assets, goos, goarch, opts.Kinds, best.Version)
	if err != nil {
		return best, false, err
	}
	best.Asset = asset
	best.Decision = DecisionAvailable
	if best.MinVersionCode > 0 && opts.CurrentCode > 0 && opts.CurrentCode < best.MinVersionCode {
		best.Decision = DecisionMandatory
	}
	return best, true, nil
}

// newerThan решает, новее ли выпуск текущей версии: по version_code, когда
// оба кода известны, иначе по SemVer (§5).
func newerThan(cur semver, curErr error, current string, curCode int64, rel Release) (bool, error) {
	if curCode > 0 && rel.VersionCode > 0 {
		return rel.VersionCode > curCode, nil
	}
	if curErr != nil {
		return false, fmt.Errorf("текущая версия: %w: %q", ErrBadVersion, current)
	}
	rv, err := parseSemver(rel.Version)
	if err != nil {
		return false, fmt.Errorf("selfupdate: версия выпуска: %w: %q", ErrBadVersion, rel.Version)
	}
	return compareSemver(rv, cur) > 0, nil
}

// selectAsset выбирает файл под платформу: первый подходящий kind в порядке
// предпочтения (§5); arch «any» подходит к любой. Пустой Kinds — любой вид в
// порядке документа.
func selectAsset(assets []Asset, goos, goarch string, kinds []string, version string) (Asset, error) {
	match := func(a Asset, kind string) bool {
		if !strings.EqualFold(a.OS, goos) {
			return false
		}
		if !strings.EqualFold(a.Arch, goarch) && !strings.EqualFold(a.Arch, "any") {
			return false
		}
		if kind == "" {
			return true
		}
		return strings.EqualFold(string(a.Kind), kind)
	}
	if len(kinds) == 0 {
		for _, a := range assets {
			if match(a, "") {
				return a, nil
			}
		}
	} else {
		for _, k := range kinds {
			for _, a := range assets {
				if match(a, k) {
					return a, nil
				}
			}
		}
	}
	return Asset{}, withCode(CodeNoAsset, fmt.Errorf("%w: %s/%s, выпуск %s", ErrNoAsset, goos, goarch, version))
}
