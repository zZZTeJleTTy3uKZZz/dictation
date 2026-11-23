package selfupdate

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"
	"time"
)

// ProtocolV1 — значение поля protocol манифеста.
const ProtocolV1 = "update/1"

// Каналы выпуска (§2). Пустое поле channel означает stable.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
	ChannelDev    = "dev"
)

// Kind — вид файла выпуска (§2).
type Kind string

// Виды файлов §2.
const (
	KindAPK       Kind = "apk"
	KindInstaller Kind = "installer"
	KindArchive   Kind = "archive"
	KindBinary    Kind = "binary"
)

// InstallerKind — вид установщика Windows (§2); задаёт тихие параметры §5.
type InstallerKind string

// Установщики §2.
const (
	InstallerInno InstallerKind = "inno"
	InstallerMSI  InstallerKind = "msi"
	InstallerNSIS InstallerKind = "nsis"
)

// Decision — решение об обновлении (§5–§6).
type Decision string

// Решения §5.
const (
	DecisionAvailable Decision = "available"
	DecisionUpToDate  Decision = "up_to_date"
	DecisionMandatory Decision = "mandatory"
)

// Manifest — документ update/1 (§2): один на приложение и канал.
//
// Прежние формы (§7) читаются и приводятся к этой: у них Protocol тоже
// ProtocolV1, а App, Channel, VersionCode и MinVersionCode заполнены по
// правилам перевода.
type Manifest struct {
	Protocol string
	App      string
	Channel  string
	Version  string
	// VersionCode — целое > 0, монотонно растёт с Version; у прежней формы
	// mybits его нет и здесь 0.
	VersionCode int64
	// MinVersionCode — с какой версии обновление обязательно; 0 — не задано.
	MinVersionCode int64
	// PublishedAt — только для показа; нулевое время — поля не было.
	PublishedAt time.Time
	Notes       string
	Assets      []Asset
}

// Release переводит документ в выпуск, с которым работают Check и Apply.
func (m Manifest) Release() Release {
	sv, _ := parseSemver(m.Version) // разбор уже проверен ParseManifest
	return Release{
		Version:        m.Version,
		Prerelease:     len(sv.pre) > 0,
		Notes:          m.Notes,
		Assets:         m.Assets,
		Protocol:       m.Protocol,
		App:            m.App,
		Channel:        m.Channel,
		VersionCode:    m.VersionCode,
		MinVersionCode: m.MinVersionCode,
		PublishedAt:    m.PublishedAt,
	}
}

// сырые поля всех форм сразу: так разбор не угадывает форму по именам полей.
type rawManifest struct {
	Protocol       string     `json:"protocol"`
	App            string     `json:"app"`
	Channel        string     `json:"channel"`
	Version        string     `json:"version"`
	VersionCode    *int64     `json:"version_code"`
	MinVersionCode int64      `json:"min_version_code"`
	PublishedAt    string     `json:"published_at"`
	Notes          string     `json:"notes"`
	Assets         []rawAsset `json:"assets"`
	// Прежняя форма ai-forms/meetdigest-android (§7): вложенная и плоская.
	Latest      *rawLatest `json:"latest"`
	VersionName string     `json:"version_name"`
	URL         string     `json:"url"`
	SHA256      string     `json:"sha256"`
	Bytes       int64      `json:"bytes"`
}

type rawAsset struct {
	Kind      string   `json:"kind"`
	OS        string   `json:"os"`
	Arch      string   `json:"arch"`
	URL       string   `json:"url"`
	SHA256    string   `json:"sha256"`
	Bytes     int64    `json:"bytes"`
	Installer string   `json:"installer"`
	Args      []string `json:"args"`
}

type rawLatest struct {
	VersionCode    *int64 `json:"version_code"`
	MinVersionCode int64  `json:"min_version_code"`
	VersionName    string `json:"version_name"`
	URL            string `json:"url"`
	SHA256         string `json:"sha256"`
	Bytes          int64  `json:"bytes"`
	Notes          string `json:"notes"`
}

// ParseManifest разбирает документ манифеста и проверяет поля (§2).
//
// Принимаются и прежние формы (§7): mybits {version, notes, assets[{os,
// arch, url, sha256}]} и ai-forms {min_version_code, latest{…}} либо плоская.
// Относительные url здесь ещё не разрешены — это делает Resolve, когда
// известен итоговый адрес манифеста.
func ParseManifest(data []byte) (Manifest, error) {
	var raw rawManifest
	if err := json.Unmarshal(data, &raw); err != nil {
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "разбор JSON: %v", err)
	}
	switch {
	case raw.Protocol != "":
		return parseV1(raw)
	case raw.Latest != nil:
		return parseLegacyAndroid(raw)
	case raw.Version == "" && (raw.VersionName != "" || raw.URL != ""):
		return parseLegacyAndroid(raw)
	case raw.Version != "" && raw.App == "" && raw.VersionCode == nil:
		return parseLegacyMybits(raw)
	default:
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "не понять форму документа")
	}
}

// parseV1 проверяет документ формы v1 (§2).
func parseV1(raw rawManifest) (Manifest, error) {
	if raw.Protocol != ProtocolV1 {
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "protocol %q, ждали %q", raw.Protocol, ProtocolV1)
	}
	if !validApp(raw.App) {
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "app %q — нужны латиница в нижнем регистре, цифры и «-»", raw.App)
	}
	channel := strings.TrimSpace(raw.Channel)
	if channel == "" {
		channel = ChannelStable
	}
	switch channel {
	case ChannelStable, ChannelBeta, ChannelDev:
	default:
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "channel %q", channel)
	}
	if _, err := parseSemver(raw.Version); err != nil {
		return Manifest{}, withCode(CodeBadVersion, fmt.Errorf("selfupdate: манифест: %w: %q", ErrBadVersion, raw.Version))
	}
	if raw.VersionCode == nil || *raw.VersionCode <= 0 {
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "version_code должен быть больше нуля")
	}
	if raw.MinVersionCode < 0 {
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "min_version_code %d", raw.MinVersionCode)
	}
	var published time.Time
	if raw.PublishedAt != "" {
		t, err := time.Parse(time.RFC3339, raw.PublishedAt)
		if err != nil {
			return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "published_at %q: %v", raw.PublishedAt, err)
		}
		published = t
	}
	if len(raw.Assets) == 0 {
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "нет ни одного файла в assets")
	}
	assets := make([]Asset, 0, len(raw.Assets))
	for i, ra := range raw.Assets {
		a, err := parseAssetV1(i, ra)
		if err != nil {
			return Manifest{}, err
		}
		assets = append(assets, a)
	}
	return Manifest{
		Protocol:       raw.Protocol,
		App:            raw.App,
		Channel:        channel,
		Version:        raw.Version,
		VersionCode:    *raw.VersionCode,
		MinVersionCode: raw.MinVersionCode,
		PublishedAt:    published,
		Notes:          raw.Notes,
		Assets:         assets,
	}, nil
}

// parseAssetV1 проверяет один файл формы v1 (§2).
func parseAssetV1(i int, ra rawAsset) (Asset, error) {
	kind := Kind(strings.ToLower(strings.TrimSpace(ra.Kind)))
	switch kind {
	case KindAPK, KindInstaller, KindArchive, KindBinary:
	default:
		return Asset{}, codef(CodeBadManifest, ErrBadManifest, "файл №%d: kind %q", i+1, ra.Kind)
	}
	os := strings.ToLower(strings.TrimSpace(ra.OS))
	switch os {
	case "android", "windows", "linux", "darwin":
	default:
		return Asset{}, codef(CodeBadManifest, ErrBadManifest, "файл №%d: os %q", i+1, ra.OS)
	}
	arch := strings.ToLower(strings.TrimSpace(ra.Arch))
	switch arch {
	case "amd64", "arm64", "any":
	default:
		return Asset{}, codef(CodeBadManifest, ErrBadManifest, "файл №%d: arch %q", i+1, ra.Arch)
	}
	if strings.TrimSpace(ra.URL) == "" {
		return Asset{}, codef(CodeBadManifest, ErrBadManifest, "файл №%d: пустой url", i+1)
	}
	if _, err := url.Parse(ra.URL); err != nil {
		return Asset{}, codef(CodeBadManifest, ErrBadManifest, "файл №%d: url %q: %v", i+1, ra.URL, err)
	}
	sha := strings.ToLower(strings.TrimSpace(ra.SHA256))
	if sha == "" {
		return Asset{}, codef(CodeNoChecksum, ErrNoChecksum, "файл №%d: пустой sha256", i+1)
	}
	if !isSHA256Hex(sha) {
		return Asset{}, codef(CodeBadManifest, ErrBadManifest, "файл №%d: sha256 %q — не 64 hex", i+1, ra.SHA256)
	}
	if ra.Bytes <= 0 {
		return Asset{}, codef(CodeBadManifest, ErrBadManifest, "файл №%d: bytes должен быть больше нуля", i+1)
	}
	installer := InstallerKind(strings.ToLower(strings.TrimSpace(ra.Installer)))
	if kind == KindInstaller {
		switch installer {
		case InstallerInno, InstallerMSI, InstallerNSIS:
		default:
			return Asset{}, codef(CodeBadManifest, ErrBadManifest, "файл №%d: installer %q", i+1, ra.Installer)
		}
	} else if installer != "" {
		return Asset{}, codef(CodeBadManifest, ErrBadManifest, "файл №%d: installer задан при kind %q", i+1, kind)
	}
	return Asset{
		Kind:      kind,
		OS:        os,
		Arch:      arch,
		Name:      fileNameFromURL(ra.URL),
		URL:       ra.URL,
		SHA256:    sha,
		Bytes:     ra.Bytes,
		Installer: installer,
		Args:      append([]string(nil), ra.Args...),
	}, nil
}

// parseLegacyMybits переводит прежнюю форму mybits в v1 (§7): kind — binary
// или archive по расширению, bytes неизвестен (сверка размера пропускается),
// version_code 0, обязательности нет.
func parseLegacyMybits(raw rawManifest) (Manifest, error) {
	if _, err := parseSemver(raw.Version); err != nil {
		return Manifest{}, withCode(CodeBadVersion, fmt.Errorf("selfupdate: манифест: %w: %q", ErrBadVersion, raw.Version))
	}
	assets := make([]Asset, 0, len(raw.Assets))
	for i, ra := range raw.Assets {
		os := strings.ToLower(strings.TrimSpace(ra.OS))
		arch := strings.ToLower(strings.TrimSpace(ra.Arch))
		if os == "" || arch == "" {
			return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "файл №%d: без os или arch", i+1)
		}
		if strings.TrimSpace(ra.URL) == "" {
			return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "файл №%d: пустой url", i+1)
		}
		sha := strings.ToLower(strings.TrimSpace(ra.SHA256))
		if sha == "" {
			return Manifest{}, codef(CodeNoChecksum, ErrNoChecksum, "файл №%d: пустой sha256", i+1)
		}
		if !isSHA256Hex(sha) {
			return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "файл №%d: sha256 %q — не 64 hex", i+1, ra.SHA256)
		}
		kind := KindBinary
		if isArchiveURL(ra.URL) {
			kind = KindArchive
		}
		assets = append(assets, Asset{
			Kind:   kind,
			OS:     os,
			Arch:   arch,
			Name:   fileNameFromURL(ra.URL),
			URL:    ra.URL,
			SHA256: sha,
		})
	}
	return Manifest{
		Protocol: ProtocolV1,
		Channel:  ChannelStable,
		Version:  raw.Version,
		Notes:    raw.Notes,
		Assets:   assets,
	}, nil
}

// parseLegacyAndroid переводит прежнюю форму ai-forms/meetdigest-android
// (вложенную и плоскую) в v1 (§7): kind apk, os android, arch any.
func parseLegacyAndroid(raw rawManifest) (Manifest, error) {
	var (
		code    int64
		name    string
		link    string
		sha     string
		bytes   int64
		notes   string
		minimum int64
	)
	minimum = raw.MinVersionCode
	if l := raw.Latest; l != nil {
		if l.VersionCode != nil {
			code = *l.VersionCode
		}
		if l.MinVersionCode > 0 {
			minimum = l.MinVersionCode
		}
		name, link, sha, bytes, notes = l.VersionName, l.URL, l.SHA256, l.Bytes, l.Notes
	} else {
		if raw.VersionCode != nil {
			code = *raw.VersionCode
		}
		name, link, sha, bytes, notes = raw.VersionName, raw.URL, raw.SHA256, raw.Bytes, raw.Notes
	}
	if _, err := parseSemver(name); err != nil {
		return Manifest{}, withCode(CodeBadVersion, fmt.Errorf("selfupdate: манифест: %w: %q", ErrBadVersion, name))
	}
	if code <= 0 {
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "version_code должен быть больше нуля")
	}
	if minimum < 0 {
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "min_version_code %d", minimum)
	}
	if bytes < 0 {
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "bytes %d", bytes)
	}
	if strings.TrimSpace(link) == "" {
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "пустой url")
	}
	if _, err := url.Parse(link); err != nil {
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "url %q: %v", link, err)
	}
	sha = strings.ToLower(strings.TrimSpace(sha))
	if sha == "" {
		return Manifest{}, codef(CodeNoChecksum, ErrNoChecksum, "пустой sha256")
	}
	if !isSHA256Hex(sha) {
		return Manifest{}, codef(CodeBadManifest, ErrBadManifest, "sha256 %q — не 64 hex", sha)
	}
	return Manifest{
		Protocol:       ProtocolV1,
		Channel:        ChannelStable,
		Version:        name,
		VersionCode:    code,
		MinVersionCode: minimum,
		Notes:          notes,
		Assets: []Asset{{
			Kind:   KindAPK,
			OS:     "android",
			Arch:   "any",
			Name:   fileNameFromURL(link),
			URL:    link,
			SHA256: sha,
			Bytes:  bytes,
		}},
	}, nil
}

// Resolve разрешает относительные url от итогового адреса манифеста base и
// проверяет схемы: только https, http — лишь для loopback (§3).
func (m *Manifest) Resolve(base *url.URL) error {
	if base == nil {
		return codef(CodeBadManifest, ErrBadManifest, "неизвестен адрес манифеста: относительные url не разрешить")
	}
	for i := range m.Assets {
		a := &m.Assets[i]
		ref, err := url.Parse(a.URL)
		if err != nil {
			return codef(CodeBadManifest, ErrBadManifest, "файл №%d: url %q: %v", i+1, a.URL, err)
		}
		abs := base.ResolveReference(ref)
		if err := checkSecureURL(abs); err != nil {
			return err
		}
		a.URL = abs.String()
		if a.Name == "" {
			a.Name = path.Base(abs.Path)
		}
		if a.Name == "" || a.Name == "." || a.Name == "/" {
			return codef(CodeBadManifest, ErrBadManifest, "файл №%d: в url нет имени", i+1)
		}
	}
	return nil
}

// checkSecureURL проверяет схему адреса по §3.
func checkSecureURL(u *url.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return codef(CodeInsecureURL, ErrInsecureURL, "%s: нужен https", displayURL(u.String()))
	default:
		return codef(CodeInsecureURL, ErrInsecureURL, "%s: схема %q, нужен https", displayURL(u.String()), u.Scheme)
	}
}

// secureURL разбирает адрес и требует https (http — только для loopback).
func secureURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, codef(CodeBadManifest, ErrBadManifest, "неверный URL %q", raw)
	}
	if err := checkSecureURL(u); err != nil {
		return nil, err
	}
	return u, nil
}

// isLoopbackHost — 127.0.0.1, ::1 и localhost: только для них в проверках
// допустим http (§3).
func isLoopbackHost(host string) bool {
	h := strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(host), "["), "]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// validApp — поле app: [a-z0-9-]+ (§2).
func validApp(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		ok := r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// fileNameFromURL — имя файла из url (без запроса и фрагмента).
func fileNameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return path.Base(u.Path)
}

// isArchiveURL — расширение пути говорит, что это архив (§7).
func isArchiveURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	p := strings.ToLower(u.Path)
	return strings.HasSuffix(p, ".tar.gz") || strings.HasSuffix(p, ".tgz") || strings.HasSuffix(p, ".zip")
}
