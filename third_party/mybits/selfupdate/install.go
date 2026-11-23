package selfupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Install скачивает и ставит файл выпуска по его виду (§5):
//
//   - binary, archive (и пустой вид прежних выпусков) — Apply;
//   - installer — тихий установщик Windows, запущенный скрытым окном;
//   - apk — не ставится: на Android это делает система, пакет только качает файл.
func Install(ctx context.Context, rel Release, opts ApplyOptions) error {
	switch Kind(strings.ToLower(string(rel.Asset.Kind))) {
	case "", KindBinary, KindArchive:
		return Apply(ctx, rel, opts)
	case KindInstaller:
		return installPackage(ctx, rel, opts)
	case KindAPK:
		return withCode(CodeInstallFailed, fmt.Errorf("%w: файл apk устанавливает система Android", ErrUnsupported))
	default:
		return codef(CodeBadManifest, ErrBadManifest, "неизвестный вид файла %q", rel.Asset.Kind)
	}
}

// installPackage скачивает установщик и запускает его (§5). Временный файл
// лежит в каталоге кэша приложения (opts.Dir), а не в %TEMP% корня диска.
func installPackage(ctx context.Context, rel Release, opts ApplyOptions) error {
	if err := checkInstallerSupport(); err != nil {
		return withCode(CodeInstallFailed, err)
	}
	if err := checkAsset(rel); err != nil {
		return err
	}
	if rel.Asset.Installer == "" {
		return codef(CodeBadManifest, ErrBadManifest, "у файла %q не задан installer", rel.Asset.Name)
	}
	limit := opts.MaxSize
	if limit <= 0 {
		limit = defaultMaxSize
	}
	dir := opts.Dir
	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return fmt.Errorf("selfupdate: каталог кэша: %w", err)
		}
		dir = filepath.Join(cache, "update")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("selfupdate: каталог загрузки %s: %w", dir, err)
	}
	pattern := "installer-*"
	if name := assetName(rel.Asset); name != "" {
		pattern = "." + filepath.Base(name) + ".download-*"
	}
	path, err := downloadVerified(ctx, rel, opts, dir, pattern, limit)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(path) }()
	return runInstaller(ctx, path, rel.Asset)
}

// installerCommand — программа и параметры тихой установки (§5). Свои args
// заменяют умолчание целиком (для msi программой остаётся msiexec).
func installerCommand(path string, a Asset) (string, []string, error) {
	if len(a.Args) > 0 {
		args := append([]string(nil), a.Args...)
		if a.Installer == InstallerMSI {
			return "msiexec.exe", args, nil
		}
		return path, args, nil
	}
	switch a.Installer {
	case InstallerInno:
		return path, []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-"}, nil
	case InstallerMSI:
		return "msiexec.exe", []string{"/i", path, "/qn", "/norestart"}, nil
	case InstallerNSIS:
		return path, []string{"/S"}, nil
	}
	return "", nil, codef(CodeBadManifest, ErrBadManifest, "установщик %q", a.Installer)
}
