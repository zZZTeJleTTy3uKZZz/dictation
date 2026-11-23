package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// extractSpec — что и сколько достать из архива.
type extractSpec struct {
	// name — базовое имя нужного бинарника (с .exe для Windows).
	name string
	// fold — сравнивать имя без учёта регистра (файловая система Windows).
	fold bool
	// limit — предел размера одного файла и суммы размеров всех файлов архива.
	limit int64
}

func (s extractSpec) match(base string) bool {
	if s.fold {
		return strings.EqualFold(base, s.name)
	}
	return base == s.name
}

// safeName переводит имя из архива к виду с «/» и отказывает при любой
// попытке выйти из каталога: «..», абсолютный путь, диск, NUL. Обратная
// косая считается разделителем: часть упаковщиков Windows пишет её в zip, и
// `a\..\..\x` — это такой же выход вверх, как `a/../../x`.
//
// Проверяется КАЖДАЯ запись архива, а не только нужная: архив, в котором
// есть хоть одна такая запись, подозрителен целиком.
func safeName(raw string) (string, error) {
	n := strings.ReplaceAll(raw, `\`, "/")
	bad := n == "" || strings.ContainsRune(n, 0) || strings.HasPrefix(n, "/") || (len(n) >= 2 && n[1] == ':')
	if !bad {
		for _, seg := range strings.Split(n, "/") {
			if seg == ".." {
				bad = true
				break
			}
		}
	}
	if bad {
		return "", fmt.Errorf("%w: %q", ErrUnsafeArchive, raw)
	}
	return n, nil
}

// errAmbiguous — в архиве два файла с нужным именем: какой из них бинарник,
// не угадать, а подсунуть второй проще, чем кажется.
func errAmbiguous(name string) error {
	return fmt.Errorf("%w: несколько файлов %q", ErrBinaryNotFound, name)
}

// extractTarGz достаёт единственный обычный файл spec.name из tar.gz в w.
// Ссылки (симлинки и жёсткие), устройства и каталоги не берутся никогда.
func extractTarGz(src string, spec extractSpec, w io.Writer) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("selfupdate: открыть загрузку: %w", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("selfupdate: архив не gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)

	var total int64
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, tar.ErrInsecurePath) {
			return fmt.Errorf("%w: %w", ErrUnsafeArchive, err)
		}
		if err != nil {
			return fmt.Errorf("selfupdate: чтение tar: %w", err)
		}
		name, err := safeName(h.Name)
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		total += h.Size
		if h.Size > spec.limit || total > spec.limit {
			return fmt.Errorf("%w: распакованное больше %d байт", ErrTooLarge, spec.limit)
		}
		if !spec.match(path.Base(name)) {
			continue
		}
		if found {
			return errAmbiguous(spec.name)
		}
		found = true
		if err := copyLimited(w, tr, spec.limit); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("%w: нет файла %q", ErrBinaryNotFound, spec.name)
	}
	return nil
}

// extractZip — то же для zip.
func extractZip(src string, spec extractSpec, w io.Writer) error {
	zr, err := zip.OpenReader(src)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return fmt.Errorf("selfupdate: архив не zip: %w", err)
	}
	if zr == nil {
		return fmt.Errorf("%w: %w", ErrUnsafeArchive, err)
	}
	defer func() { _ = zr.Close() }()

	var total int64
	found := false
	for _, zf := range zr.File {
		name, err := safeName(zf.Name)
		if err != nil {
			return err
		}
		if !zf.Mode().IsRegular() {
			continue // каталоги, симлинки, устройства
		}
		if zf.UncompressedSize64 > uint64(spec.limit) {
			return fmt.Errorf("%w: распакованное больше %d байт", ErrTooLarge, spec.limit)
		}
		total += int64(zf.UncompressedSize64)
		if total > spec.limit {
			return fmt.Errorf("%w: распакованное больше %d байт", ErrTooLarge, spec.limit)
		}
		if !spec.match(path.Base(name)) {
			continue
		}
		if found {
			return errAmbiguous(spec.name)
		}
		found = true
		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("selfupdate: чтение %q из zip: %w", zf.Name, err)
		}
		err = copyLimited(w, rc, spec.limit)
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("%w: нет файла %q", ErrBinaryNotFound, spec.name)
	}
	return nil
}

// copyLimited копирует не больше limit байт; больше — ErrTooLarge (заголовок
// архива мог соврать о размере).
func copyLimited(w io.Writer, r io.Reader, limit int64) error {
	n, err := io.Copy(w, io.LimitReader(r, limit+1))
	if err != nil {
		return fmt.Errorf("selfupdate: распаковка: %w", err)
	}
	if n > limit {
		return fmt.Errorf("%w: распакованное больше %d байт", ErrTooLarge, limit)
	}
	return nil
}
