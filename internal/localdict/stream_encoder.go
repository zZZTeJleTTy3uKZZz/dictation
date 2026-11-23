package localdict

import (
	"os"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/opusrec"
)

// compressChunk сжимает фразу диктовки в Ogg/Opus тем же кодировщиком, что пишет
// и саму запись, и отдаёт путь к файлу.
//
// Раньше кусок писался во временный WAV и уходил в разбор несжатым: на
// четырнадцать секунд речи это четыреста сорок килобайт, и разбор платил за
// лишний вес временем. Потом его сжимал помощник Windows (Media Foundation,
// AAC в m4a) — отдельный процесс на каждую фразу, с той же бедой, что у записи:
// файл получает оглавление только при закрытии. Теперь сжатие внутри процесса,
// и в облако уходит готовый Ogg/Opus.
//
// Отказ кодировщика не должен ронять разбор: не вышло — пишем WAV, как прежде.
func (a *App) compressChunk(chunk []byte) (string, error) {
	file, err := os.CreateTemp(a.Dir, "phrase-*.ogg")
	if err != nil {
		return "", err
	}
	path := file.Name()
	_ = file.Close()
	_ = os.Remove(path)
	recorder, err := opusrec.Start(path)
	if err != nil {
		return writeChunkWave(strings.TrimSuffix(path, ".ogg"), chunk)
	}
	if err := recorder.Write(chunk); err != nil {
		recorder.Abort()
		_ = os.Remove(path)
		return writeChunkWave(strings.TrimSuffix(path, ".ogg"), chunk)
	}
	if closed := recorder.Close(); closed == "" {
		return writeChunkWave(strings.TrimSuffix(path, ".ogg"), chunk)
	}
	return path, nil
}

// writeChunkWave — прежний путь: несжатый кусок. Остаётся запасным ходом.
func writeChunkWave(base string, chunk []byte) (string, error) {
	path := base + ".wav"
	if err := WriteWave(path, chunk); err != nil {
		return "", err
	}
	return path, nil
}
