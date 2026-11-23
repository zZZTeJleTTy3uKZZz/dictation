package aacrecover

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// bytesPerSample — проба PCM, которую просим у декодера: 16 бит, моно.
const bytesPerSample = 2

// bytesPerFrame — сколько байт PCM занимает один кадр AAC-LC (1024 отсчёта).
const bytesPerFrame = FrameSamples * bytesPerSample

// Decoder — чем проверяется звук.
//
// Два действия: первое отдаёт звук из куска (декодер сам читает несколько
// кадров подряд), второе только считает кадры. Счёт нужен для поиска конца
// последнего целого кадра в куске — звук там уже не интересен.
type Decoder interface {
	// Decode декодирует data[pos:pos+length] (внутри — несколько кадров AAC
	// подряд, упакованных в одно поле ADTS) и отдаёт PCM 16 кГц моно s16.
	// frames — сколько целых кадров получилось. Хвост короче кадра в счёт не
	// идёт: декодер на нём ругается, а кадр не отдаёт.
	Decode(data []byte, pos, length int) (pcm []byte, frames int, err error)
	// Frames — сколько целых кадров в тех же байтах, без звука.
	Frames(data []byte, pos, length int) (int, error)
}

// FFmpeg — декодер через внешний ffmpeg.
//
// ПОЧЕМУ ВНЕШНИЙ. Своего декодера AAC в зависимостях нет, а писать его ради
// восстановления старых записей — недели работы ради одной поломки. ffmpeg
// программе уже нужен для сведения дорожек встречи, поэтому лишней
// зависимости не появляется. Цена — запуск процесса на кусок: около 50–100 мс,
// и на трёхминутную запись восстановление идёт минуты, а не секунды. Это
// приемлемо: путь разовый, для старых файлов, и идёт в фоне.
type FFmpeg struct {
	// Path — имя или полный путь программы.
	Path string
	// Timeout ограничивает один вызов: зависший ffmpeg не должен останавливать
	// восстановление навсегда.
	Timeout time.Duration
}

// NewFFmpeg — декодер с путём по умолчанию и разумным пределом ожидания.
func NewFFmpeg(path string) *FFmpeg {
	return &FFmpeg{Path: path, Timeout: 2 * time.Minute}
}

// FindFFmpeg ищет ffmpeg рядом с программой, а затем в PATH.
//
// Рядом — потому что пакет может принести помощника с собой; в PATH — потому
// что на машинах, где встречи сводятся, он уже стоит.
func FindFFmpeg() (string, error) {
	if executable, err := os.Executable(); err == nil {
		nearby := filepath.Join(filepath.Dir(executable), "ffmpeg.exe")
		if info, err := os.Stat(nearby); err == nil && !info.IsDir() {
			return nearby, nil
		}
	}
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", ErrNoDecoder
	}
	return path, nil
}

// Decode декодирует кусок и отдаёт звук.
func (f *FFmpeg) Decode(data []byte, pos, length int) ([]byte, int, error) {
	if pos < 0 || length < MinFrameBytes || pos+length > len(data) {
		return nil, 0, nil
	}
	pcm, _, err := f.run(Wrap(data[pos : pos+length]))
	if err != nil {
		return nil, 0, err
	}
	return pcm, len(pcm) / bytesPerFrame, nil
}

// Frames — сколько кадров в куске.
func (f *FFmpeg) Frames(data []byte, pos, length int) (int, error) {
	_, frames, err := f.Decode(data, pos, length)
	return frames, err
}

// run декодирует поток ADTS в PCM.
//
// Код возврата ffmpeg не считается отказом: в куске законно не хватает конца
// последнего кадра, и ffmpeg на этом ругается, но целые кадры отдаёт — их и
// нужно. Отказом считается только невозможность запустить программу: без неё
// восстанавливать нечем.
func (f *FFmpeg) run(stream []byte) ([]byte, []byte, error) {
	if f.Path == "" {
		return nil, nil, ErrNoDecoder
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := commandContext(ctx, f.Path,
		"-v", "error",
		"-f", "aac",
		"-i", "pipe:0",
		"-f", "s16le",
		"-ac", "1",
		"-ar", fmt.Sprint(SampleRate),
		"pipe:1",
	)
	command.Stdin = bytes.NewReader(stream)
	var pcm, diagnostics bytes.Buffer
	command.Stdout = &pcm
	command.Stderr = &diagnostics
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, nil, fmt.Errorf("ffmpeg не ответил за %s: %w", timeout, ctx.Err())
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, nil, ErrNoDecoder
		}
		// Программа запустилась и отказала — это её мнение о данных, а не
		// отсутствие декодера. Кадров из такого потока просто не вышло.
	}
	return pcm.Bytes(), diagnostics.Bytes(), nil
}
