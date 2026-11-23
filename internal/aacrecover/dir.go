package aacrecover

import (
	"os"
	"path/filepath"
	"time"
)

// Outcome — что вышло с одной оборванной записью в папке.
type Outcome struct {
	// Source — файл .m4a, который пытались восстановить.
	Source string
	// Audio — полученный WAV; пусто, если восстановить не удалось.
	Audio  string
	Result Result
	// Err — причина отказа; nil, если звук восстановлен.
	Err error
}

// RecoverDir восстанавливает оборванные дорожки в папке записей.
//
// Что пропускается и почему:
//   - файл моложе minAge — это может быть идущая запись: у неё оглавление
//     появится при закрытии, и вырывать звук из-под пишущего помощника нельзя;
//   - файл с оглавлением — он целый, восстановление ему не нужно;
//   - рядом уже лежит .wav — восстановление было раньше, второй раз это минуты
//     работы декодера.
//
// Отказы возвращаются в Outcome, а не обрывают обход: одна битая запись не
// должна мешать восстановлению соседних.
func RecoverDir(dir string, decoder Decoder, minAge time.Duration) ([]Outcome, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.m4a"))
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	var outcomes []Outcome
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			outcomes = append(outcomes, Outcome{Source: path, Err: err})
			continue
		}
		if minAge > 0 && time.Since(info.ModTime()) < minAge {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			outcomes = append(outcomes, Outcome{Source: path, Err: err})
			continue
		}
		if HasIndex(data) {
			continue
		}
		result, target, err := RecoverFile(path, decoder)
		if err == ErrAlreadyRecovered {
			continue
		}
		outcomes = append(outcomes, Outcome{Source: path, Audio: target, Result: result, Err: err})
	}
	return outcomes, nil
}
