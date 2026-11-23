package localdict

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Recognition — итог разбора записи: чем разобрано и что сказано. Seconds —
// сколько ждал человек, а не только разбор: по этому числу видно цену способа.
type Recognition struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Text    string  `json:"text"`
	Seconds float64 `json:"seconds"`
	Error   string  `json:"error,omitempty"`
}

// errAudioNotFound — записи нет на диске: это не отказ разбора, а неверный путь.
var errAudioNotFound = errors.New("аудиофайл не найден")

// recordingCompressTimeout — срок на сжатие одной записи. Полчаса звука
// кодируются за две секунды, так что срок здесь только на случай, когда
// помощник почему-то встал: ждать его вечно нельзя, а запись важнее сжатия.
const recordingCompressTimeout = 2 * time.Minute

// maxTranscribeBytes is the largest recording the daemon accepts from outside.
// Two hours of sixteen-kilohertz mono sound take a hundred and fifteen
// megabytes, and a person who talks into someone else's window for half an hour
// must not be cut off at the end: the limit is here to bound what one request can
// make the machine hold, not to decide how long a thought may be.
const maxTranscribeBytes = 256 << 20

// TranscribeFile is dictation for another program: a recording arrives from
// outside and the words go back.
//
// Разбор только облачный: местного распознавателя в диктовке больше нет (решение
// владельца), и отказ облака называется причиной, а не подменяется моделью на
// машине. Способ берётся тот, которым человек диктует, и словарь терминов — тот
// же: чужая программа должна слышать то же, что и своя.
func (a *App) TranscribeFile(ctx context.Context, audio string) (Recognition, error) {
	a.mu.Lock()
	override := a.transcribeOverride
	a.mu.Unlock()
	if override != nil {
		return override(ctx, audio)
	}
	if info, err := os.Stat(audio); err != nil || info.IsDir() {
		return Recognition{}, errAudioNotFound
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Остановка службы должна останавливать и чужой разбор: он запущен с этим
	// сроком и убивается вместе с ним.
	stop := context.AfterFunc(a.ctx, cancel)
	defer stop()
	result, err, attempted := a.networkAttempt(ctx, audio)
	if !attempted || err != nil {
		if err != nil {
			return Recognition{}, cloudRefusal(err)
		}
		return Recognition{}, errCloudUnavailable
	}
	return result, nil
}

// recordingFormat узнаёт запись по её же первым байтам и называет расширение,
// под которым её хранить. Пришедшее с чем-то другим надо отвергнуть до запуска
// распознавателя: движку, которому дали мусор, отвечает мусор, а причина теряется
// по дороге.
//
// Сжатый звук больше не отвергается. Harness пишет диктовку в Opus и отдаёт её
// в контейнере WebM; пересобирать это в WAV значило бы разбирать и кодировать
// заново то, что уже готово, — и терять на этом время, которого человек ждёт.
// Сервис принимает такой звук как есть, а хранить его дешевле несжатого: час
// речи в Opus — единицы мегабайт против ста пятнадцати в WAV.
//
// Сравниваются байты, а не слова. WebM начинается с 1A 45 DF A3, а слово EBML
// стоит дальше, внутри заголовка EBML, в поле DocType: узнавать по нему контейнер
// — всё равно что узнавать человека по слову «человек» в его паспорте.
func recordingFormat(data []byte) (string, bool) {
	if len(data) < 4 {
		return "", false
	}
	// MP4 и его звук (.m4a) сигнатурой не начинаются: первые четыре байта —
	// размер блока, а признак лежит следом ("ftyp"). Без этой проверки сжатая
	// запись, которую мы сами и пишем, разбором отвергалась бы.
	if len(data) >= 8 && string(data[4:8]) == "ftyp" {
		return ".m4a", true
	}
	switch string(data[0:4]) {
	case "RIFF":
		if len(data) >= 12 && string(data[8:12]) == "WAVE" {
			return ".wav", true
		}
	case "\x1a\x45\xdf\xa3": // EBML: с этих четырёх байтов начинается WebM
		return ".webm", true
	case "OggS":
		return ".ogg", true
	case "fLaC":
		return ".flac", true
	}
	return "", false
}

// keepRecording кладёт запись чужой программы рядом с нашими. Разбор может не
// получиться — нет модели, отказал движок, кончился срок, — и тогда остаётся
// звук, который человек послушает сам. Потерять сказанное из-за отказа чужой
// программы недопустимо: текст можно набрать заново, а голос уже не вернуть.
//
// Хранится запись в том виде, в каком пришла. Прежде она здесь же сжималась в
// MP3 — и сжатый Opus от Harness сжимался бы второй раз, теряя качество ради
// ничего. Сжимаем только несжатое.
func (a *App) keepRecording(body []byte) string {
	dir := filepath.Join(a.Media, "recordings")
	if os.MkdirAll(dir, 0700) != nil {
		return ""
	}
	extension, _ := recordingFormat(body)
	// Имя начинается с даты, как у наших записей: по нему же решается, какие
	// записи старые, и чужой час не должен вытеснять наши раньше времени.
	path := filepath.Join(dir, time.Now().Format("20060102-150405")+"-plugin"+extension)
	if os.WriteFile(path, body, 0600) != nil {
		return ""
	}
	if extension == ".wav" {
		path = a.compressRecording(path)
	}
	// Предел один на все расширения и на всю папку записей: иначе каждый
	// формат держит свой предел, а вместе их втрое больше задуманного.
	pruneRecordings(dir, recordingLimit)
	return path
}

// compressRecording сжимает запись помощником Windows. Час звука в том виде, в
// котором его ест распознаватель, — это сто пятнадцать мегабайт, и история из
// получасовых диктовок съела бы диск за неделю; в MP3 тот же час занимает
// одиннадцать. Кодировщик живёт в самой Windows, и зовём мы его отдельной
// программой, как и распознаватель: тащить его внутрь процесса значило бы
// привязать сборку к чужим библиотекам.
//
// Отказ сжатия не должен стоить записи. Нет помощника, нет кодировщика в этой
// редакции Windows, кончился срок — остаётся WAV, и сказанное всё равно можно
// послушать.
func (a *App) compressRecording(path string) string {
	if path == "" {
		return ""
	}
	// Сжатое не сжимаем второй раз. За одной записью сюда приходят дважды:
	// сперва перед расшифровкой, потом когда кладут разговор в историю. Первый
	// заход сжимает и убирает исходный файл, и второй получает на вход готовый
	// mp3. Упаковщик этого не умеет и отказывает — безвредно, но зря: на каждой
	// диктовке запускалась программа, которой нечего делать, а в журнал ложилась
	// строка «compression failed=true stage=run», по которой потом искали
	// несуществующую поломку. Найдено живьём 14 октября.
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3", ".ogg", ".opus", ".m4a", ".flac", ".aac":
		return path
	}
	executable, err := os.Executable()
	if err != nil {
		return path
	}
	helper := filepath.Join(filepath.Dir(executable), "audio-compress.exe")
	if _, err := os.Stat(helper); err != nil {
		return path
	}
	var before int64
	if info, err := os.Stat(path); err == nil {
		before = info.Size()
	}
	// Длительность исходника — до сжатия: после него WAV уже удалён, и сверять
	// будет не с чем.
	sourceSeconds, sourceKnown := wavSeconds(path)
	target := strings.TrimSuffix(path, filepath.Ext(path))
	ctx, cancel := context.WithTimeout(a.ctx, recordingCompressTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, helper, path, target)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	// Свой вывод помощника читаем сами, а не через Output(): при отказе важно
	// не только «упало», но и код возврата с первой строкой его ошибки.
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if err != nil {
		code, line := commandFailure(err, stderr.String(), stdout.String())
		a.sessionLog("compression", fmt.Sprintf("failed=true stage=run exit=%d error=%q", code, line))
		return path
	}
	output := stdout.Bytes()
	var answer struct {
		Path    string  `json:"path"`
		Format  string  `json:"format"`
		Bytes   int64   `json:"bytes"`
		Seconds float64 `json:"seconds"`
		Error   string  `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output), &answer); err != nil {
		a.sessionLog("compression", fmt.Sprintf("failed=true stage=answer error=%q raw=%q", err.Error(), firstLine(string(output))))
		return path
	}
	if answer.Error != "" || answer.Path == "" {
		a.sessionLog("compression", fmt.Sprintf("failed=true stage=answer error=%q path=%q", firstLine(answer.Error), answer.Path))
		return path
	}
	if _, err := os.Stat(answer.Path); err != nil {
		a.sessionLog("compression", fmt.Sprintf("failed=true stage=missing path=%q", answer.Path))
		return path
	}
	// Сверка длительности: сжатая копия короче записи означает, что кодировщик
	// оборвался или соврал, и удалять WAV в этом случае нельзя — человек
	// потеряет часть сказанного молча.
	if sourceKnown && !sameDuration(sourceSeconds, answer.Seconds) {
		_ = os.Remove(answer.Path)
		a.sessionLog("compression", fmt.Sprintf(
			"failed=true stage=duration in_seconds=%.2f out_seconds=%.2f", sourceSeconds, answer.Seconds))
		return path
	}
	// Сжатая запись встаёт на место несжатой: две копии одного разговора
	// человеку не нужны, а место занимает именно большая.
	_ = os.Remove(path)
	a.sessionLog("compression", fmt.Sprintf("bytes_in=%d bytes_out=%d format=%s seconds=%.1f", before, answer.Bytes, answer.Format, answer.Seconds))
	return answer.Path
}

// rememberPlugged дописывает разговор чужой программы в общую историю: человек
// смотрит её в одном месте, и запись, сделанная микрофоном Harness, ничем не
// отличается от надиктованной нашим сочетанием.
func (a *App) rememberPlugged(text, audio string, seconds float64) {
	a.mu.Lock()
	entry := historyEntry(text, audio, "harness", seconds, statusDone, "", false)
	if a.addHistoryLocked(entry, true) {
		a.touchHistoryLocked()
	}
	a.mu.Unlock()
	a.saveHistory()
}

// commandFailure — код возврата помощника и первая строка его ошибки.
//
// Без них в журнале оставалось «compression failed=true stage=run», и по нему
// нельзя было отличить отказ кодировщика от неверного довода: причина не
// сохранялась нигде, а искали её потом по четырнадцати одинаковым строкам.
func commandFailure(err error, stderr, stdout string) (int, string) {
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	line := firstLine(stderr)
	if line == "" {
		line = firstLine(stdout)
	}
	if line == "" {
		line = firstLine(err.Error())
	}
	return code, line
}

// firstLine — первая непустая строка текста: в журнале нужна причина, а не
// весь вывод помощника.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
