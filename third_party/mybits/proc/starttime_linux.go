//go:build linux

package proc

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// userHZ — единица времени /proc/<pid>/stat: USER_HZ, часть ABI ядра (100).
const userHZ = 100

func startTime(pid int) (time.Time, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, err
	}
	// Имя процесса в скобках может содержать пробелы и скобки: поля считаем
	// после последней «)».
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 {
		return time.Time{}, fmt.Errorf("proc: /proc/%d/stat не разобран", pid)
	}
	fields := strings.Fields(string(raw[i+1:]))
	// Поле 22 (starttime) — 20-е после «)» (первое после «)» — поле 3, state).
	if len(fields) < 20 {
		return time.Time{}, fmt.Errorf("proc: /proc/%d/stat короткий", pid)
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	boot, err := bootTime()
	if err != nil {
		return time.Time{}, err
	}
	return boot.Add(time.Duration(ticks) * time.Second / userHZ).UTC(), nil
}

func bootTime() (time.Time, error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			sec, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(sec, 0), nil
		}
	}
	return time.Time{}, ErrNoStartTime
}
