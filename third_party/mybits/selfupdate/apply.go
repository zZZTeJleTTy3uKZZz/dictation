package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// defaultMaxSize — предел скачивания и распаковки по умолчанию (§4: 1 ГБ).
const defaultMaxSize = 1 << 30

// ApplyOptions — настройки замены.
type ApplyOptions struct {
	// Executable — какой файл заменять; пусто — запущенная программа
	// (os.Executable с раскрытыми ссылками).
	Executable string
	// Binary — имя бинарника в архиве без .exe; пусто — имя Executable без .exe.
	Binary string
	// HTTPClient для скачивания, если у выпуска нет своего способа
	// (Release из ReleasesSource скачивает сам, с токеном); nil — http.DefaultClient.
	HTTPClient *http.Client
	// MaxSize — предел скачивания и распаковки в байтах; 0 — 1 ГБ.
	MaxSize int64
	// Progress получает (получено, всего) во время скачивания; всего —
	// это bytes манифеста (0, если размер неизвестен). Необязателен.
	Progress func(received, total int64)
	// Dir — каталог для временного файла установщика; пусто — кэш
	// пользователя. Для binary/archive временный файл всегда рядом с заменяемым.
	Dir string
	// Verify — необязательная проба нового бинарника до замены, например
	// запуск с --version. Отказ отменяет замену.
	Verify func(ctx context.Context, path string) error
}

// readerFunc превращает функцию в io.Reader.
type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// progressReader считает полученное и зовёт Progress.
type progressReader struct {
	r        io.Reader
	got      int64
	total    int64
	progress func(received, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.got += int64(n)
	if p.progress != nil && n > 0 {
		p.progress(p.got, p.total)
	}
	return n, err
}

// Apply скачивает файл rel.Asset, проверяет размер и sha256, достаёт бинарник
// и подменяет им opts.Executable. Выпуск нужно получить из Check (там выбран
// Asset).
//
// Шаги, и при сбое любого из них прежний бинарник остаётся нетронутым, а
// временные файлы рядом с ним удаляются:
//
//  1. нет суммы sha256 — ErrNoChecksum, ничего не скачивается;
//  2. загрузка во временный файл рядом с программой с подсчётом суммы,
//     прогрессом и пределом MaxSize; больше bytes манифеста — сразу
//     size_mismatch, меньше — тоже (§4); сверка суммы (ErrChecksum) ДО любой
//     распаковки;
//  3. распаковка (.tar.gz/.tgz, .zip; иной файл считается самим бинарником):
//     один обычный файл с нужным именем на любой глубине; «..», абсолютные
//     пути и ссылки — ErrUnsafeArchive и отказ всего архива;
//  4. проба opts.Verify на новом файле;
//  5. замена: Linux/macOS — атомарный rename поверх; Windows — запущенный
//     .exe переименовывается в «.exe.old», новый встаёт на его место (убрать
//     «.old» — CleanupOld при следующем запуске).
//
// Запущенный процесс не перезапускается: новая версия начнёт работать со
// следующего старта.
func Apply(ctx context.Context, rel Release, opts ApplyOptions) error {
	return applyWith(ctx, rel, opts, runtime.GOOS == "windows", nil)
}

// applyWith — Apply с подменяемыми стилем замены и переименователем (для проверок).
func applyWith(ctx context.Context, rel Release, opts ApplyOptions, windowsStyle bool, rename func(from, to string) error) (err error) {
	if err := checkAsset(rel); err != nil {
		return err
	}
	asset := rel.Asset
	switch asset.Kind {
	case KindInstaller, KindAPK:
		return codef(CodeBadManifest, ErrBadManifest, "файл вида %q ставит Install, а не Apply", asset.Kind)
	}

	exe := opts.Executable
	if exe == "" {
		if exe, err = currentExecutable(); err != nil {
			return err
		}
	}
	info, err := os.Stat(exe)
	if err != nil {
		return fmt.Errorf("selfupdate: заменяемый файл: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("selfupdate: %s — не обычный файл", exe)
	}
	limit := opts.MaxSize
	if limit <= 0 {
		limit = defaultMaxSize
	}

	dir, base := filepath.Split(exe)
	if dir == "" {
		dir = "."
	}
	binary := opts.Binary
	if binary == "" {
		binary = base
	}
	binary = trimExe(binary)
	target := strings.EqualFold(asset.OS, "windows") || hasExe(base) || hasExe(opts.Binary)
	spec := extractSpec{name: binary, fold: target, limit: limit}
	if target {
		spec.name += ".exe"
	}

	// 2. Загрузка с прогрессом и сверкой размера и суммы.
	dlPath, err := downloadVerified(ctx, rel, opts, dir, "."+base+".download-*", limit)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(dlPath) }()

	// 3. Распаковка рядом с программой.
	nf, err := os.CreateTemp(dir, "."+base+".new-*")
	if err != nil {
		return fmt.Errorf("selfupdate: временный файл для бинарника: %w", err)
	}
	newPath := nf.Name()
	defer func() { _ = os.Remove(newPath) }() // после успешной замены файла уже нет

	if err := unpack(dlPath, assetName(asset), spec, nf); err != nil {
		_ = nf.Close()
		return err
	}
	if err := nf.Sync(); err != nil {
		_ = nf.Close()
		return fmt.Errorf("selfupdate: сброс нового бинарника на диск: %w", err)
	}
	if err := nf.Close(); err != nil {
		return fmt.Errorf("selfupdate: закрытие нового бинарника: %w", err)
	}
	if err := os.Chmod(newPath, info.Mode().Perm()|0o111); err != nil {
		return fmt.Errorf("selfupdate: права нового бинарника: %w", err)
	}

	// 4. Проба (файл уже закрыт: запуск открытого на запись даёт ETXTBSY).
	if opts.Verify != nil {
		if err := opts.Verify(ctx, newPath); err != nil {
			return fmt.Errorf("selfupdate: проверка нового бинарника: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return withCode(CodeCancelled, fmt.Errorf("%w: %w", ErrCancelled, err))
	}

	// 5. Замена.
	return replace(exe, newPath, windowsStyle, rename)
}

// checkAsset проверяет, что у выпуска есть файл и его сумма, до скачивания.
func checkAsset(rel Release) error {
	asset := rel.Asset
	if asset.URL == "" {
		return withCode(CodeNoAsset, fmt.Errorf("%w: у выпуска %q не выбран файл (сначала Check)", ErrNoAsset, rel.Version))
	}
	want := strings.ToLower(strings.TrimSpace(asset.SHA256))
	if want == "" {
		return codef(CodeNoChecksum, ErrNoChecksum, "%s", asset.Name)
	}
	if !isSHA256Hex(want) {
		return codef(CodeChecksumMismatch, ErrChecksum, "%q — не sha256 в hex", asset.SHA256)
	}
	return nil
}

// downloadVerified скачивает ассет в новый временный файл в dir и проверяет
// размер и sha256. Возвращает путь; файл удаляет вызывающий.
func downloadVerified(ctx context.Context, rel Release, opts ApplyOptions, dir, pattern string, limit int64) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("selfupdate: временный файл для загрузки: %w", err)
	}
	path := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(path)
		}
	}()
	want := strings.ToLower(strings.TrimSpace(rel.Asset.SHA256))
	if err := download(ctx, rel, opts, f, want, limit); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("selfupdate: сброс загрузки на диск: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("selfupdate: закрытие загрузки: %w", err)
	}
	ok = true
	return path, nil
}

// download качает файл выпуска в dst, считая sha256 и сообщая прогресс;
// размер сверяется с bytes манифеста, сумма — до любой установки (§4).
func download(ctx context.Context, rel Release, opts ApplyOptions, dst io.Writer, wantSum string, limit int64) error {
	open := rel.fetch
	if open == nil {
		open = newFetcher(opts.HTTPClient, nil, "").open
	}
	rc, err := open(ctx, rel.Asset.URL)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	total := rel.Asset.Bytes
	if opts.Progress != nil {
		opts.Progress(0, total)
	}
	readLimit := limit + 1
	if total > 0 && total+1 < readLimit {
		readLimit = total + 1
	}
	h := sha256.New()
	// Отмена проверяется между чтениями: источник, не знающий о ctx, тоже
	// останавливается.
	pr := &progressReader{r: rc, total: total, progress: opts.Progress}
	src := readerFunc(func(p []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return pr.Read(p)
	})
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, readLimit))
	if err != nil {
		if ctx.Err() != nil {
			return withCode(CodeCancelled, fmt.Errorf("%w: %w", ErrCancelled, ctx.Err()))
		}
		return withCode(CodeDownloadFailed, fmt.Errorf("%w: загрузка %s: %w", ErrDownloadFailed, displayURL(rel.Asset.URL), err))
	}
	if cerr := ctx.Err(); cerr != nil {
		return withCode(CodeCancelled, fmt.Errorf("%w: %w", ErrCancelled, cerr))
	}
	if n > limit {
		return codef(CodeTooLarge, ErrTooLarge, "загрузка больше %d байт", limit)
	}
	if total > 0 && n != total {
		return codef(CodeSizeMismatch, ErrSizeMismatch, "ждали %d байт, получили %d", total, n)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSum {
		return codef(CodeChecksumMismatch, ErrChecksum, "ждали %s, получили %s", wantSum, got)
	}
	return nil
}

// unpack достаёт бинарник из загрузки по расширению имени ассета.
func unpack(src, name string, spec extractSpec, dst io.Writer) error {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTarGz(src, spec, dst)
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(src, spec, dst)
	}
	// Не архив: сам файл и есть бинарник.
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("selfupdate: открыть загрузку: %w", err)
	}
	defer func() { _ = f.Close() }()
	return copyLimited(dst, f, spec.limit)
}

// assetName — имя файла ассета; если источник его не назвал, берётся из url.
func assetName(a Asset) string {
	if a.Name != "" {
		return a.Name
	}
	if u, err := url.Parse(a.URL); err == nil {
		return filepath.Base(u.Path)
	}
	return ""
}

func hasExe(s string) bool { return strings.HasSuffix(strings.ToLower(s), ".exe") }

func trimExe(s string) string {
	if hasExe(s) {
		return s[:len(s)-4]
	}
	return s
}
