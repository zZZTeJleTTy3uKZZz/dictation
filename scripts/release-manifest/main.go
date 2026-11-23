// Package main собирает публичный манифест обновления по протоколу update/1
// и список контрольных сумм релиза (контракт 2026-11-19-public-release.md, §2).
//
// В публичный релиз идут архив платформы, install.ps1, update.json и
// SHA256SUMS. Генератор живёт в публичном дереве: его можно запустить и
// проверить без доступа к приватным модулям. Адрес источника — публичный
// GitHub Releases, токен не нужен.
//
// Вызов:
//
//	go run ./scripts/release-manifest \
//	  -version 1.13.0 -notes "Диктовка 1.13.0" \
//	  -archive build/release/dictation-windows-amd64.zip \
//	  -base-url https://github.com/zZZTeJleTTy3uKZZz/dictation/releases/download/v1.13.0 \
//	  -extra install.ps1 -out build/release
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	protocol = "update/1"
	app      = "meetdigest-dictation"
	channel  = "stable"

	kind    = "archive"
	platOS  = "windows"
	platArc = "amd64"

	// Установщик Windows (Inno Setup) — второй ассет выпуска; вид и поле
	// installer читает клиент обновлений (протокол update/1, §2).
	kindInstaller = "installer"
	installerInno = "inno"
)

type asset struct {
	Kind   string `json:"kind"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	// Installer — вид установщика; только для kind installer.
	Installer string `json:"installer,omitempty"`
}

type manifest struct {
	Protocol    string  `json:"protocol"`
	App         string  `json:"app"`
	Channel     string  `json:"channel"`
	Version     string  `json:"version"`
	VersionCode int64   `json:"version_code"`
	Notes       string  `json:"notes"`
	Assets      []asset `json:"assets"`
}

type stringsFlag []string

func (s *stringsFlag) String() string { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error {
	if v != "" {
		*s = append(*s, v)
	}
	return nil
}

func main() {
	version := flag.String("version", "", "версия выпуска X.Y.Z (обязательно)")
	notes := flag.String("notes", "", "что нового")
	archive := flag.String("archive", "", "архив платформы (обязательно)")
	installer := flag.String("installer", "", "установщик Windows Inno Setup (необязательно): попадёт в манифест как kind installer")
	baseURL := flag.String("base-url", "", "префикс ссылки на файлы релиза (обязательно)")
	out := flag.String("out", "", "каталог для update.json и SHA256SUMS (обязательно)")
	var extra stringsFlag
	flag.Var(&extra, "extra", "дополнительный файл релиза для SHA256SUMS (можно повторять)")
	flag.Parse()

	if err := run(*version, *notes, *archive, *installer, *baseURL, *out, extra); err != nil {
		fmt.Fprintln(os.Stderr, "release-manifest:", err)
		os.Exit(1)
	}
}

func run(version, notes, archivePath, installerPath, baseURL, out string, extra []string) error {
	if version == "" || archivePath == "" || baseURL == "" || out == "" {
		return fmt.Errorf("нужны -version, -archive, -base-url и -out")
	}
	code, err := versionCode(version)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(baseURL, "https://") {
		return fmt.Errorf("-base-url должен быть https-адресом")
	}

	sums := map[string]string{}
	addSum := func(path string) (int64, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", path, err)
		}
		digest := sha256.Sum256(data)
		sums[filepath.Base(path)] = hex.EncodeToString(digest[:])
		return int64(len(data)), nil
	}
	bytes, err := addSum(archivePath)
	if err != nil {
		return err
	}
	for _, path := range extra {
		if _, err := addSum(path); err != nil {
			return err
		}
	}

	base := strings.TrimRight(baseURL, "/")
	assets := []asset{{
		Kind:   kind,
		OS:     platOS,
		Arch:   platArc,
		URL:    base + "/" + filepath.Base(archivePath),
		SHA256: sums[filepath.Base(archivePath)],
		Bytes:  bytes,
	}}
	if installerPath != "" {
		installerBytes, err := addSum(installerPath)
		if err != nil {
			return err
		}
		assets = append(assets, asset{
			Kind:      kindInstaller,
			OS:        platOS,
			Arch:      platArc,
			URL:       base + "/" + filepath.Base(installerPath),
			SHA256:    sums[filepath.Base(installerPath)],
			Bytes:     installerBytes,
			Installer: installerInno,
		})
	}
	man := manifest{
		Protocol:    protocol,
		App:         app,
		Channel:     channel,
		Version:     version,
		VersionCode: code,
		Notes:       notes,
		Assets:      assets,
	}
	body, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "update.json"), append(body, '\n'), 0o644); err != nil {
		return err
	}
	if err := writeSums(filepath.Join(out, "SHA256SUMS"), sums); err != nil {
		return err
	}
	fmt.Printf("protocol=%s\nversion=%s\nassets=%d\nsums=%d\nout=%s\n", protocol, version, len(man.Assets), len(sums), out)
	return nil
}

// versionCode переводит SemVer в монотонное целое: 1.13.0 → 11300.
func versionCode(version string) (int64, error) {
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("версия %q должна быть X.Y.Z", version)
	}
	nums := [3]int64{}
	for i, part := range parts {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil || n < 0 || n > 99 {
			return 0, fmt.Errorf("версия %q: неверная часть %q", version, part)
		}
		nums[i] = n
	}
	return nums[0]*10000 + nums[1]*100 + nums[2], nil
}

func writeSums(path string, sums map[string]string) error {
	names := make([]string, 0, len(sums))
	for name := range sums {
		names = append(names, name)
	}
	sort.Strings(names)
	var lines strings.Builder
	for _, name := range names {
		fmt.Fprintf(&lines, "%s  %s\n", sums[name], name)
	}
	return os.WriteFile(path, []byte(lines.String()), 0o644)
}
