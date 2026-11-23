//go:build windows

package audioenc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// Encoder сжимает звук по ходу речи, а не после её остановки.
//
// Зачем это нужно: раньше запись ложилась на диск несжатой, и уже потом
// отдельный заход упаковывал её в mp3. На длинной диктовке ожидание упаковки
// прибавлялось к ожиданию распознавания, хотя кодировать можно было всё время,
// пока человек говорит. Кодировщик умеет это с самого начала — Media Foundation
// пишет по куску, — но вход у него был только из готового файла.
//
// Помощник audio-compress.exe теперь принимает звук на вход (режим --stream) и
// отдаёт готовый mp3 к тому моменту, когда во вход упирается конец файла.
// Проверено: тот же звук, прогнанный файловым и потоковым путём, даёт побайтово
// одинаковый mp3.
//
// Отказы здесь никогда не должны ронять диктовку: если помощник не нашёлся, не
// запустился или отказал, вызывающий просто кладёт запись прежним путём. Ради
// этого все ошибки возвращаются наружу, а не пишутся в журнал молча.
type Encoder struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr streamStderr

	base   string // путь без расширения: помощник сам дописывает расширение
	codec  Codec  // чем пишет: от этого зависит расширение и читаемость по ходу
	rate   int
	closed bool
	mu     sync.Mutex

	written int64 // сколько байт звука ушло помощнику
}

// compressAnswer — ответ помощника, та же строка JSON, что и у файлового режима.
type compressAnswer struct {
	Path    string  `json:"path"`
	Format  string  `json:"format"`
	Bytes   int64   `json:"bytes"`
	Seconds float64 `json:"seconds"`
	Error   string  `json:"error"`
}

// Helper — помощник сжатия лежит рядом с самой программой, как и
// распознаватель. Каталог берётся у запущенного файла, а не у рабочего каталога:
// служба поднимается задачей планировщика, и рабочий каталог у неё чужой.
func Helper() string {
	executable, err := os.Executable()
	if err != nil {
		return "audio-compress.exe"
	}
	return filepath.Join(filepath.Dir(executable), "audio-compress.exe")
}

// Start запускает помощника в потоковом режиме. name — имя файла без
// расширения в каталоге dir; rate — частота звука, который будет подаваться.
func Start(helper, dir, name string, rate int) (*Encoder, error) {
	return StartWith(helper, dir, name, rate, 0)
}

// Codec — чем помощник сжимает звук.
//
// ПОЧЕМУ ЭТО ПАРАМЕТР, А НЕ КОНСТАНТА. Измерено 18.10.2026 на развёрнутом
// помощнике: AAC он пишет в MP4, а у MP4 оглавление (moov) ложится в КОНЕЦ при
// закрытии — то есть во время записи файла на диске нет вовсе, и отдать его
// куда-либо по ходу встречи нечего. MP3 того же помощника пишет кадрами: файл
// растёт с первой секунды, и недописанная копия (12 203 байта, снятая на
// середине) развернулась в 3,93 с звука без единой ошибки.
//
// Значит выбор кодека — это выбор «можно ли читать запись во время записи», а не
// вкусовщина: AAC нужен там, где файл читают только целиком (диктовка), MP3 —
// там, где его отдают по ходу (встреча).
type Codec string

const (
	// CodecAAC — AAC в .m4a. Меньше на доли процента, но НЕ читается до закрытия.
	CodecAAC Codec = "aac"
	// CodecMP3 — MP3. Читается и декодируется, пока файл ещё пишется.
	CodecMP3 Codec = "mp3"
)

// Extension — какое расширение помощник допишет к имени. Нужно вызывающему:
// по нему он находит файл, пока тот ещё не закрыт (например, чтобы отдать его
// по ходу встречи).
func (c Codec) Extension() string {
	if c == CodecMP3 {
		return ".mp3"
	}
	return ".m4a"
}

// encoderArgs — доводы помощнику. Отдельной функцией, чтобы их можно было
// проверить без запуска: сам помощник в проверке не поднять (он часть Windows),
// а доводы — то место, где уже была поломка.
//
// ПОЧЕМУ ЗДЕСЬ НЕТ --kbps. Измерено 17.10.2026 на развёрнутом помощнике: с
// `--kbps 20000` он падает и не пишет ничего — «mf-set-input 0xC00D36B4», ноль
// байт. А значения, которые он принимает (20, 24, 32, 64, 128, 24000), дают ОДИН
// И ТОТ ЖЕ размер: довод он не слушает и пишет своим умолчанием. То есть довод не
// уменьшал запись, зато ронял её целиком — и запись ложилась в WAV, 691 МБ в час
// вместо 9. Без довода помощник пишет ~24,8 кбит/с: 92 872 байта за 30 секунд на
// 16 кГц моно, то есть 11,1 МБ в час и 22,3 МБ за два часа при пределе 25 МБ.
//
// Битрейт остаётся в подписи нарочно: он часть договора с вызывающим, и если
// помощник научится его слушать, довод вернётся сюда — но не раньше, чем это
// будет измерено.
func encoderArgs(base string, rate, bitrate int, codec Codec) []string {
	_ = bitrate
	if codec == "" {
		codec = CodecAAC // пустое значение — прежнее поведение, не сюрприз
	}
	return []string{
		"--stream",
		"--out", base,
		"--rate", strconv.Itoa(rate),
		"--codec", string(codec),
	}
}

// StartWith — то же, но с явным битрейтом в битах в секунду (0 — умолчание
// помощника). Довод у помощника называется --kbps, а понимается как биты в
// секунду: килобиты он молча заменяет умолчанием, и это уже стоило нам
// неверного замера.
// Path — файл, который помощник пишет ПРЯМО СЕЙЧАС.
//
// Нужен затем, чтобы отдать растущую запись по ходу встречи: у MP3 она читается
// и декодируется, не дожидаясь закрытия. У AAC этот путь указывает на файл,
// которого до закрытия не существует, — и это не ошибка метода, а свойство
// кодека (см. `Codec`).
func (e *Encoder) Path() string {
	return e.base + e.codec.Extension()
}

func StartWith(helper, dir, name string, rate, bitrate int) (*Encoder, error) {
	return StartWithCodec(helper, dir, name, rate, bitrate, CodecAAC)
}

// StartWithCodec — то же, но с явным кодеком. Кодек выбирает вызывающий, потому
// что требование к нему зависит от задачи: диктовке файл нужен целиком, а
// встрече — читаемый по ходу.
func StartWithCodec(helper, dir, name string, rate, bitrate int, codec Codec) (*Encoder, error) {
	if rate <= 0 {
		return nil, fmt.Errorf("stream encoder: rate %d", rate)
	}
	if _, err := os.Stat(helper); err != nil {
		return nil, fmt.Errorf("stream encoder: помощник не найден: %w", err)
	}
	if codec == "" {
		codec = CodecAAC
	}
	base := filepath.Join(dir, name)
	encoder := &Encoder{base: base, rate: rate, codec: codec}
	// Кодек и битрейт называются явно. Без этого помощник брал mp3 по умолчанию,
	// и запись ложилась бы .mp3 — мимо и сжатого вида, и поиска записей.
	//
	// Довод называется --kbps, а помощник понимает его как биты в секунду: 24
	// молча заменяются значением по умолчанию, поэтому здесь полное число.
	args := encoderArgs(base, rate, bitrate, codec)
	encoder.cmd = exec.Command(helper, args...)
	// Окно помощнику не нужно: служба работает без окон, и промелькнувшее
	// чёрное окно человек прочитал бы как поломку.
	encoder.cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	stdin, err := encoder.cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stream encoder: вход: %w", err)
	}
	stdout, err := encoder.cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stream encoder: выход: %w", err)
	}
	encoder.stdin, encoder.stdout = stdin, stdout
	encoder.cmd.Stderr = &encoder.stderr
	if err := encoder.cmd.Start(); err != nil {
		return nil, fmt.Errorf("stream encoder: запуск: %w", err)
	}
	return encoder, nil
}

// write отдаёт помощнику очередную порцию звука. Порция может быть любой длины:
// внутри помощника она всё равно нарезается по секунде.
func (e *Encoder) Write(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return fmt.Errorf("stream encoder: запись после закрытия")
	}
	if _, err := e.stdin.Write(pcm); err != nil {
		return fmt.Errorf("stream encoder: запись звука: %w", err)
	}
	e.written += int64(len(pcm))
	return nil
}

// finish закрывает вход и дожидается ответа помощника. Готовый mp3 к этому
// моменту уже лежит на диске — ждать, пока он закодируется, не приходится.
func (e *Encoder) Finish() (string, error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return "", fmt.Errorf("stream encoder: уже закрыт")
	}
	e.closed = true
	stdin, stdout := e.stdin, e.stdout
	e.mu.Unlock()

	if err := stdin.Close(); err != nil {
		e.Abort()
		return "", fmt.Errorf("stream encoder: закрытие входа: %w", err)
	}
	answer, err := io.ReadAll(stdout)
	waitErr := e.cmd.Wait()
	if err != nil {
		return "", fmt.Errorf("stream encoder: чтение ответа: %w", err)
	}
	var parsed compressAnswer
	trimmed := bytes.TrimSpace(answer)
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return "", fmt.Errorf("stream encoder: разбор ответа %q: %w", string(trimmed), err)
	}
	if parsed.Error != "" {
		return "", fmt.Errorf("stream encoder: %s", parsed.Error)
	}
	if waitErr != nil {
		return "", fmt.Errorf("stream encoder: помощник вышел с ошибкой: %w", waitErr)
	}
	if parsed.Path == "" {
		return "", fmt.Errorf("stream encoder: помощник не назвал файл")
	}
	return parsed.Path, nil
}

// abort бросает начатую работу: помощник получает конец входа и сам убирает за
// собой недописанный файл. Нужен на всяком пути отказа, чтобы не оставлять
// половинчатых записей.
func (e *Encoder) Abort() {
	e.mu.Lock()
	stdin := e.stdin
	closed := e.closed
	e.closed = true
	e.mu.Unlock()
	if stdin != nil && !closed {
		_ = stdin.Close()
	}
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
		_, _ = e.cmd.Process.Wait()
	}
	_ = os.Remove(e.base + e.codec.Extension())
}

// streamStderr копит поток ошибок помощника: он уходит в журнал только тогда,
// когда что-то действительно пошло не так.
type streamStderr struct {
	mu   sync.Mutex
	data []byte
}

func (b *streamStderr) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.data) < 4096 {
		b.data = append(b.data, p...)
	}
	return len(p), nil
}

func (b *streamStderr) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
