// Package config — настройки воркера рабочей машины.
//
// Порт meetdigest_watcher/config.py. Читаются из ~/.meetdigest/watcher.env —
// того же файла, что у Python-версии, и с теми же именами переменных: на
// рабочих машинах он уже лежит, и переименование ради красоты сломало бы
// установку у живых людей.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/appdirs"
)

// DefaultWatchDir — куда Телемост кладёт записи по умолчанию.
//
// Оно же признак «человек ничего не выбирал»: тогда папку ищем сами по
// обычным местам.
const DefaultWatchDir = "~/Documents/Телемост"

type Config struct {
	// APIURL — адрес сервиса. Публичный домен, а не петля: с тех пор как API
	// отвечает наружу по HTTPS, ssh-туннель не нужен.
	APIURL string
	// APIKey — ключ со правами jobs:write и jobs:read. Без него делать нечего.
	APIKey string
	// WatchDirs — что прописано переменной; пусто или значение по умолчанию
	// означает «спроси у настроек» (sources.json).
	WatchDirs string
	// Extensions — что считается записью.
	Extensions []string
	// Interval — как часто обходить папки.
	Interval float64
	// MaxAgeHours — архив прошлых месяцев не трогаем: первый запуск не должен
	// выгрести его целиком.
	MaxAgeHours float64
	// DeviceName — как машина подписана в списке устройств; пусто — сетевое имя.
	DeviceName string
	// CatchMeetings — следить ли за микрофоном вовсе.
	CatchMeetings bool
	// StatePath — где лежит тетрадь состояния для значка в лотке.
	StatePath string
	// LogFile — куда пишется журнал.
	LogFile string
}

// Home — папка настроек воркера.
func Home() string {
	dir := appdirs.HomeDir()
	if dir == "" {
		return ".meetdigest"
	}
	return filepath.Join(dir, ".meetdigest")
}

// EnvFile — путь к файлу настроек.
func EnvFile() string { return filepath.Join(Home(), "watcher.env") }

// Load — настройки из файла и окружения.
//
// Переменная окружения главнее файла: разовый запуск можно переопределить,
// ничего не правя.
func Load(path string) (*Config, error) {
	if path == "" {
		path = EnvFile()
	}
	fromFile, err := readEnvFile(path)
	if err != nil {
		return nil, err
	}

	value := func(name, fallback string) string {
		if live := os.Getenv("MEETDIGEST_" + name); live != "" {
			return live
		}
		if stored, ok := fromFile["MEETDIGEST_"+name]; ok && stored != "" {
			return stored
		}
		return fallback
	}

	made := &Config{
		// Публичное умолчание — пустой адрес: приватный сервис в исходники не
		// зашивается. Свои машины задают адрес в watcher.env или MEETDIGEST_API_URL.
		APIURL:        strings.TrimRight(value("API_URL", ""), "/"),
		APIKey:        value("WATCH_API_KEY", ""),
		WatchDirs:     value("WATCH_DIRS", DefaultWatchDir),
		Extensions:    splitList(value("WATCH_EXTENSIONS", ".webm;.mp4;.mkv;.mp3;.m4a;.ogg;.wav")),
		Interval:      number(value("WATCH_INTERVAL_SEC", "30"), 30),
		MaxAgeHours:   number(value("WATCH_MAX_AGE_HOURS", "24"), 24),
		DeviceName:    value("DEVICE_NAME", ""),
		CatchMeetings: boolean(value("CATCH_MEETINGS", "true"), true),
		StatePath:     expand(value("WATCH_STATE", filepath.Join(Home(), "watcher-state.json"))),
		LogFile:       expand(value("LOG_FILE", filepath.Join(Home(), "watcher.log"))),
	}
	if made.APIKey == "" {
		return nil, fmt.Errorf("нужен ключ: MEETDIGEST_WATCH_API_KEY в %s", path)
	}
	return made, nil
}

// readEnvFile — разбор watcher.env.
//
// Файла может не быть: настройки тогда берутся из окружения, и это рабочий
// случай — так воркер запускают в проверках и на сервере.
func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("прочитать настройки: %w", err)
	}
	defer func() { _ = file.Close() }()

	found := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		// Кавычки вокруг значения снимаем: люди их ставят по привычке из shell.
		value = strings.Trim(value, `"'`)
		found[strings.TrimSpace(name)] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("дочитать настройки: %w", err)
	}
	return found, nil
}

// Save — записать значение в watcher.env, не потеряв остальные строки.
//
// Правка построчно, а не перезапись целиком: в файле лежат комментарии,
// которые человек читает, и ключ доступа, который нельзя потерять.
func Save(path, name, value string) error {
	if path == "" {
		path = EnvFile()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("завести папку настроек: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("прочитать настройки: %w", err)
	}

	lines := strings.Split(string(raw), "\n")
	full := "MEETDIGEST_" + name
	replaced := false
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), full+"=") {
			lines[index] = full + "=" + value
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, full+"="+value)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return fmt.Errorf("записать настройки: %w", err)
	}
	return nil
}

func splitList(raw string) []string {
	parts := strings.Split(raw, ";")
	found := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			found = append(found, part)
		}
	}
	return found
}

func number(raw string, fallback float64) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func boolean(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on", "да":
		return true
	case "0", "false", "no", "off", "нет":
		return false
	default:
		return fallback
	}
}

// expand — развернуть тильду в путь к домашней папке.
func expand(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home := appdirs.HomeDir()
	if home == "" {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(os.PathSeparator)))
}

// Expand — развернуть тильду; нужен и другим пакетам.
func Expand(path string) string { return expand(path) }

// DeviceName — как машина подписана в списке устройств и в истории заметок.
//
// Общее место, а не копия в каждой программе: воркер, значок и диктофон обязаны
// называть машину одинаково, иначе в списке устройств появятся два компьютера
// вместо одного, а заметки разъедутся по «источникам».
func DeviceName(chosen string) string {
	if strings.TrimSpace(chosen) != "" {
		return strings.TrimSpace(chosen)
	}
	name, err := os.Hostname()
	if err != nil {
		return "рабочая машина"
	}
	return name
}
