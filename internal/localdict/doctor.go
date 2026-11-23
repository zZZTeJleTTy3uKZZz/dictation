package localdict

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/proc"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/appdirs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/prefs"
	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/speech"
)

// Лекарь диктовки.
//
// Диктовка — не одна программа, а четыре: служба с надзирателем, значок, окно и
// плагин Harness. Отказать может любая, а человек видит только «не пишет» и не
// знает, кого винить. Поэтому лекарь проходит по всем сразу: отвечает ли
// служба, свежи ли её сведения, на месте ли модель, читаются ли ключи, свободно
// ли сочетание, совпадают ли копии плагина, целы ли настройки, стоит ли задача
// планировщика.
//
// Что можно починить без человека — чинит: поднимает службу, переписывает
// настройки без метки порядка байтов, возвращает автозапуск. Чего нельзя —
// говорит прямо, одной строкой по-русски: ключ за человека не вписать,
// занятое сочетание не освободить, модель без сети не скачать. Молчащий
// помощник человек замечает через день, а непонятную отписку — никогда.
type Check struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	Detail string `json:"detail"`
}

const (
	CheckOK      = "ok"
	CheckFixed   = "fixed"
	CheckTrouble = "trouble"
	CheckSkipped = "skipped"
)

// Doctor проверяет всё, что стоит на машине, и чинит, что может.
func Doctor(ctx context.Context) []Check {
	checks := []Check{
		doctorService(ctx),
		doctorEndpoint(),
		doctorSpeech(ctx),
		doctorKeys(),
		doctorHotkey(),
		doctorPlugins(),
		doctorSettings(),
		doctorTask(),
	}
	return checks
}

// DoctorReportPath — куда лекарь кладёт свой отчёт.
//
// Отчёт пишется всегда, даже когда его некуда напечатать: `dictation.exe`
// собран без консоли, и запущенный из проводника лекарь не сказал бы ничего —
// а молчащий лекарь хуже отсутствующего.
func DoctorReportPath() string {
	return filepath.Join(SettingsDir(), "doctor.log")
}

// WriteDoctorReport кладёт отчёт на диск. Не вышло — не беда: отчёт для
// человека, а не для работы диктовки.
func WriteDoctorReport(text string) {
	if err := os.MkdirAll(SettingsDir(), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(DoctorReportPath(), []byte(text), 0o600)
}

// AttachConsole цепляется к консоли, из которой запустили программу.
//
// Программа без консоли (наш `dictation.exe`) своего вывода не показывает:
// запустивший её человек видит пустоту и решает, что команда не работает.
// Прицепившись к чужой консоли, мы говорим туда же, куда говорил бы обычный
// консольный собрат.
//
// Но только если говорить больше некуда: когда вывод уже перенаправлен в трубу
// (`dictation.exe doctor | more`), подмена вывода на консоль выбросила бы отчёт
// из трубы — а его там ждут.
func AttachConsole() {
	if _, err := os.Stdout.Stat(); err == nil {
		return
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	attach := kernel32.NewProc("AttachConsole")
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if answer, _, _ := attach.Call(attachParentProcess); answer == 0 {
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = out
	}
	if in, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = in
	}
}

// PrintDoctor печатает проверки по-русски: по строке на каждую, а в конце —
// отдельно то, чего лекарь починить не смог. Возвращает код возврата: ноль,
// когда человеку делать нечего, и единицу, когда за что-то надо взяться самому.
func PrintDoctor(checks []Check, out io.Writer) int {
	for _, check := range checks {
		mark := "ок"
		switch check.Result {
		case CheckFixed:
			mark = "починил"
		case CheckTrouble:
			mark = "беда"
		case CheckSkipped:
			mark = "не смотрел"
		}
		fmt.Fprintf(out, "%-9s %s: %s\n", mark, check.Name, check.Detail)
	}
	trouble := Troubles(checks)
	if len(trouble) == 0 {
		fmt.Fprintln(out, "всё на месте — чинить нечего")
		return 0
	}
	fmt.Fprintln(out, "чего не могу починить сам:")
	for _, check := range trouble {
		fmt.Fprintf(out, "  — %s: %s\n", check.Name, check.Detail)
	}
	return 1
}

// Troubles — то, чего лекарь починить не может: человеку нужно знать это
// отдельно от остальных строк, иначе он прочтёт список целиком и не поймёт, за
// что браться.
func Troubles(checks []Check) []Check {
	var trouble []Check
	for _, check := range checks {
		if check.Result == CheckTrouble {
			trouble = append(trouble, check)
		}
	}
	return trouble
}

func doctorService(ctx context.Context) Check {
	name := "служба диктовки"
	dir := DataDir()
	endpoint, err := publishedEndpoint(dir)
	if err == nil && processAlive(endpoint.PID) && healthy(ctx, endpoint) {
		return Check{Name: name, Result: CheckOK, Detail: fmt.Sprintf("отвечает, pid %d", endpoint.PID)}
	}
	exe := serviceExecutable()
	if exe == "" {
		return Check{Name: name, Result: CheckTrouble, Detail: "программы службы нет на месте — поставьте диктовку заново"}
	}
	if err := startService(exe); err != nil {
		return Check{Name: name, Result: CheckTrouble, Detail: "не удалось запустить службу: " + err.Error()}
	}
	// Служба поднимается не мгновенно: сперва движок, потом разговор о порте.
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return Check{Name: name, Result: CheckTrouble, Detail: "запуск прерван"}
		case <-time.After(time.Second):
		}
		endpoint, err = publishedEndpoint(dir)
		if err == nil && processAlive(endpoint.PID) && healthy(ctx, endpoint) {
			return Check{Name: name, Result: CheckFixed, Detail: fmt.Sprintf("была не поднята — запустил, отвечает pid %d", endpoint.PID)}
		}
	}
	return Check{Name: name, Result: CheckTrouble, Detail: "не поднялась за 25 секунд — посмотрите " + filepath.Join(SettingsDir(), "supervisor.log")}
}

func doctorEndpoint() Check {
	name := "сведения о службе"
	endpoint, err := publishedEndpoint(DataDir())
	if err != nil {
		return Check{Name: name, Result: CheckTrouble, Detail: "файла endpoint.json нет: служба не публиковала адрес"}
	}
	if !processAlive(endpoint.PID) {
		return Check{Name: name, Result: CheckTrouble, Detail: fmt.Sprintf("в файле pid %d, а такого процесса нет — служба перезапускается", endpoint.PID)}
	}
	return Check{Name: name, Result: CheckOK, Detail: fmt.Sprintf("адрес %s, pid %d жив", endpoint.URL, endpoint.PID)}
}

// doctorSpeech — чем разбирается диктовка. Местной модели в диктовке больше
// нет, поэтому лекарь смотрит только облачный способ.
func doctorSpeech(ctx context.Context) Check {
	return doctorCloudRecognition(ctx)
}

// doctorCloudRecognition проверяет облачный способ: адрес, ключ (не печатая
// его) и лёгкий запрос к сервису. Человек должен видеть, чем и куда идёт его
// речь, — «по сети» без адреса ничего не объясняет.
func doctorCloudRecognition(ctx context.Context) Check {
	name := "распознавание"
	channel := doctorChannel()
	if channel == nil {
		return Check{Name: name, Result: CheckTrouble, Detail: "облако не готово: не вписан ключ или адрес сервиса — проверьте способ в настройках"}
	}
	address, model, key, ok := doctorTarget(channel)
	if !ok || strings.TrimSpace(address) == "" {
		return Check{Name: name, Result: CheckTrouble, Detail: "облако не готово: не удалось собрать адрес запроса"}
	}
	if strings.TrimSpace(key) == "" {
		return Check{Name: name, Result: CheckTrouble, Detail: "облако " + model + " через " + doctorHost(address) + ": ключ не вписан"}
	}
	probe, cancel := context.WithTimeout(ctx, cloudProbeTimeout)
	defer cancel()
	// Тот же клиент с прокси, что и у диктовки: из сети, где прямой запрос не
	// доходит, лекарь иначе сказал бы «не отвечает», хотя сервис через прокси
	// работает. Неподнявшийся прокси виден по своей причине в «не отвечает»:
	// поле прокси в окне покажет, что именно вписано.
	client, _, _ := speech.RecognitionClient(speech.ProvidersPath(), 0)
	if !cloudProbe(probe, channel, client) {
		return Check{Name: name, Result: CheckTrouble, Detail: "облако " + model + " через " + doctorHost(address) + ": не отвечает — проверьте интернет и ключ"}
	}
	return Check{Name: name, Result: CheckOK, Detail: "облако " + model + " через " + doctorHost(address) + ": адрес и ключ вписаны, сервис ответил"}
}

// doctorChannel собирает облачный способ без запуска службы: лекарь — отдельная
// программа и может работать, когда службы нет.
func doctorChannel() speech.Channel {
	return cloudChannel(prefs.LoadVoice(""), nil, nil, nil)
}

// doctorTarget — адрес, модель и ключ облачного способа. Ключ печатать нельзя:
// он только проверяется на пустоту.
func doctorTarget(channel speech.Channel) (address, model, key string, ok bool) {
	switch one := channel.(type) {
	case *speech.Cloud:
		where, name, found, why := one.Where()
		if why != "" {
			return "", "", "", false
		}
		return where, name, found, true
	case *speech.OpenAICompatible:
		return one.URL, one.Model, one.Key, true
	}
	return "", "", "", false
}

// doctorHost — узел адреса для строки лекаря.
func doctorHost(address string) string {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || parsed.Hostname() == "" {
		return "неизвестный адрес"
	}
	return parsed.Hostname()
}

func doctorKeys() Check {
	name := "ключи"
	// В диктовке остался один путь — облако, и важен ключ именно его: записи
	// чужих провайдеров к разбору речи больше не относятся.
	channel := doctorChannel()
	if channel == nil {
		return Check{Name: name, Result: CheckTrouble, Detail: "ключ облака не вписан — проверьте способ распознавания в настройках"}
	}
	_, _, key, ok := doctorTarget(channel)
	if !ok || strings.TrimSpace(key) == "" {
		return Check{Name: name, Result: CheckTrouble, Detail: "ключ облака не вписан — проверьте способ распознавания в настройках"}
	}
	return Check{Name: name, Result: CheckOK, Detail: "ключ облака на месте (сам ключ не печатается)"}
}

func doctorHotkey() Check {
	name := "сочетание клавиш"
	combo := prefs.DefaultHotkey
	if voice := prefs.LoadVoice(prefs.VoicePath()); voice != nil && strings.TrimSpace(voice.Hotkey) != "" {
		combo = voice.Hotkey
	}
	home := appdirs.HomeDir()
	if home == "" {
		return Check{Name: name, Result: CheckSkipped, Detail: "не нашёл домашнюю папку, а журнал значка лежит в ней"}
	}
	log := filepath.Join(home, ".meetdigest", "tray.log")
	data, err := os.ReadFile(log)
	if err != nil {
		return Check{Name: name, Result: CheckSkipped, Detail: "значок ещё не писал в tray.log: он либо не запускался, либо не нажимали"}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	for at := len(lines) - 1; at >= 0 && at > len(lines)-400; at-- {
		line := lines[at]
		if strings.Contains(line, "сочетание занято") {
			return Check{Name: name, Result: CheckTrouble, Detail: combo + " занято другой программой — смените его в настройках диктовки"}
		}
		if strings.Contains(line, "слушаю сочетание") {
			return Check{Name: name, Result: CheckOK, Detail: combo + " слушает значок"}
		}
	}
	return Check{Name: name, Result: CheckSkipped, Detail: "в журнале значка нет строки про сочетание — значок не докладывал"}
}

// doctorPlugins сверяет копии плагина, которые видит сама программа. Копий
// четыре — у разработчика они лежат в трёх местах и ещё одна на ноутбуке, — и
// расходятся они молча: человек диктует, а работает старая. Где искать вторую
// копию, программа не знает, поэтому спрашивает у настройки.
func doctorPlugins() Check {
	name := "копии плагина"
	live := ""
	if home := appdirs.HomeDir(); home != "" {
		live = filepath.Join(home, ".dsh", "plugins", "voice-input")
	}
	if live == "" {
		return Check{Name: name, Result: CheckSkipped, Detail: "не нашёл домашнюю папку"}
	}
	if _, err := os.Stat(filepath.Join(live, "index.js")); err != nil {
		return Check{Name: name, Result: CheckSkipped, Detail: "плагина нет в " + live + ": он ставится отдельно от диктовки"}
	}
	mirror := strings.TrimSpace(os.Getenv("DICTATION_PLUGIN_MIRROR"))
	if mirror == "" {
		return Check{Name: name, Result: CheckSkipped, Detail: "сверять не с чем: укажите DICTATION_PLUGIN_MIRROR — папку второй копии"}
	}
	same := true
	missing := ""
	for _, file := range []string{"index.js", "client.js"} {
		one, err := hashFile(filepath.Join(live, file))
		if err != nil {
			missing = file
			break
		}
		two, err := hashFile(filepath.Join(mirror, file))
		if err != nil || one != two {
			same = false
			missing = file
			break
		}
	}
	if same {
		return Check{Name: name, Result: CheckOK, Detail: "живая копия и вторая совпадают по содержимому"}
	}
	return Check{Name: name, Result: CheckTrouble, Detail: "копии разошлись (" + missing + ") — разложите свежую по всем местам"}
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}

// doctorSettings ищет метку порядка байтов в начале файла настроек. Go её не
// разбирает, а испорченный файл настроек молча отдаёт умолчания — и человек
// видит в настройках одно сочетание, а значок слушает другое. Чинится это без
// человека: содержимое остаётся тем же, уходит только метка.
func doctorSettings() Check {
	name := "настройки"
	var broken []string
	for _, path := range settingsFiles() {
		fixed, err := stripBOM(path)
		if err != nil {
			continue
		}
		if fixed {
			broken = append(broken, filepath.Base(path))
		}
	}
	if len(broken) == 0 {
		return Check{Name: name, Result: CheckOK, Detail: "файлы без метки порядка байтов"}
	}
	return Check{Name: name, Result: CheckFixed, Detail: "убрал метку порядка байтов: " + strings.Join(broken, ", ")}
}

func settingsFiles() []string {
	home := appdirs.HomeDir()
	if home == "" {
		return []string{filepath.Join(SettingsDir(), "config.json")}
	}
	root := filepath.Join(home, ".meetdigest")
	files := []string{
		filepath.Join(SettingsDir(), "config.json"),
		filepath.Join(SettingsDir(), "providers.json"),
	}
	for _, name := range []string{"voice.json", "sources.json", "catcher.json", "recording.json", "display.json", "watcher.env"} {
		files = append(files, filepath.Join(root, name))
	}
	return files
}

func stripBOM(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if len(data) < 3 || data[0] != 0xEF || data[1] != 0xBB || data[2] != 0xBF {
		return false, nil
	}
	if err := os.WriteFile(path, data[3:], 0600); err != nil {
		return false, err
	}
	return true, nil
}

// doctorTask смотрит задачу планировщика: ею служба поднимается после входа
// человека, и без неё диктовка живёт до первой перезагрузки. Поставить задачу
// лекарь не может — её ставит установщик, — но вернуть значку автозапуск может.
func doctorTask() Check {
	name := "задача планировщика"
	command := exec.Command("schtasks", "/query", "/tn", taskName)
	proc.Hide(command) // mybits/proc
	if err := command.Run(); err == nil {
		return Check{Name: name, Result: CheckOK, Detail: "«" + taskName + "» есть"}
	}
	exe := serviceExecutable()
	if exe == "" {
		return Check{Name: name, Result: CheckTrouble, Detail: "задачи «" + taskName + "» нет, и программы службы тоже — поставьте диктовку заново"}
	}
	repaired, err := EnsureAutostart(exe)
	if err != nil {
		return Check{Name: name, Result: CheckTrouble, Detail: "задачи нет, и автозапуск не починился: " + err.Error()}
	}
	if repaired {
		return Check{Name: name, Result: CheckFixed, Detail: "задачи «" + taskName + "» нет — вернул автозапуск при входе, задачу ставит установщик"}
	}
	return Check{Name: name, Result: CheckTrouble, Detail: "задачи «" + taskName + "» нет — её ставит установщик, сам я не могу"}
}

// taskName — имя задачи, которую ставит установщик.
const taskName = "Dictation Service"

func publishedEndpoint(dir string) (Endpoint, error) {
	data, err := os.ReadFile(filepath.Join(dir, "endpoint.json"))
	if err != nil {
		return Endpoint{}, err
	}
	var endpoint Endpoint
	if err := json.Unmarshal(data, &endpoint); err != nil {
		return Endpoint{}, err
	}
	if endpoint.PID == 0 || endpoint.URL == "" {
		return Endpoint{}, fmt.Errorf("сведения о службе пусты")
	}
	return endpoint, nil
}

func healthy(ctx context.Context, endpoint Endpoint) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", endpoint.URL+"/health", nil)
	if err != nil {
		return false
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode == http.StatusOK
}

// serviceExecutable — где лежит сама служба: лекарь может быть и ядром, которое
// живёт в другой папке.
func serviceExecutable() string {
	if self, err := os.Executable(); err == nil && strings.EqualFold(filepath.Base(self), "dictation.exe") {
		return self
	}
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		path := filepath.Join(dir, "Programs", "Dictation", "dictation.exe")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// startService поднимает надзирателя отдельно от лекаря: лекарь кончится, а
// служба должна остаться. Потому и DETACHED_PROCESS, а не обычный запуск.
func startService(exe string) error {
	command := exec.Command(exe, "--supervise")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008}
	return command.Start()
}
