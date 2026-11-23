package selfupdate

import (
	"fmt"
	"strconv"
	"strings"
)

// semver — версия по SemVer 2.0 в минимальном объёме, который нужен для
// сравнения выпусков. Метаданные сборки (+build) отбрасываются: по
// спецификации они на порядок не влияют.
type semver struct {
	major, minor, patch uint64
	pre                 []string
}

// parseSemver разбирает «v1.2.3», «1.2.3-rc.1», «1.2.3+build». Строже, чем
// принято в быту, сознательно: «1.2», «01.2.3» и «dev» — не версии.
func parseSemver(s string) (semver, error) {
	var v semver
	rest := strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		if err := checkIdentifiers(rest[i+1:], false); err != nil {
			return v, fmt.Errorf("selfupdate: версия %q: метаданные сборки: %w", s, err)
		}
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		if err := checkIdentifiers(rest[i+1:], true); err != nil {
			return v, fmt.Errorf("selfupdate: версия %q: pre-release: %w", s, err)
		}
		v.pre = strings.Split(rest[i+1:], ".")
		rest = rest[:i]
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("selfupdate: версия %q: нужен вид MAJOR.MINOR.PATCH", s)
	}
	nums := [3]*uint64{&v.major, &v.minor, &v.patch}
	for i, p := range parts {
		if !isNumeric(p) || (len(p) > 1 && p[0] == '0') {
			return semver{}, fmt.Errorf("selfupdate: версия %q: %q — не число без ведущих нулей", s, p)
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return semver{}, fmt.Errorf("selfupdate: версия %q: %q слишком велико", s, p)
		}
		*nums[i] = n
	}
	return v, nil
}

// checkIdentifiers проверяет список идентификаторов через точку: непустые,
// только [0-9A-Za-z-]; в pre-release числовые — без ведущих нулей.
func checkIdentifiers(s string, pre bool) error {
	if s == "" {
		return fmt.Errorf("пусто")
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return fmt.Errorf("пустой идентификатор")
		}
		for _, r := range id {
			ok := r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			if !ok {
				return fmt.Errorf("недопустимый символ %q", r)
			}
		}
		if pre && isNumeric(id) && len(id) > 1 && id[0] == '0' {
			return fmt.Errorf("числовой идентификатор %q с ведущим нулём", id)
		}
	}
	return nil
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// compareSemver возвращает -1, 0 или 1 по правилам SemVer 2.0 (пункт 11).
func compareSemver(a, b semver) int {
	for _, p := range [3][2]uint64{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1 // выпуск старше своих pre-release
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := compareIdentifier(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.pre) < len(b.pre):
		return -1
	case len(a.pre) > len(b.pre):
		return 1
	}
	return 0
}

// compareIdentifier: числовые меньше строковых, числовые сравниваются как
// числа (по длине, затем по строке — без переполнения), строковые — побайтно.
func compareIdentifier(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}
