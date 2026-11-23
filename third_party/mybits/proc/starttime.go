package proc

import (
	"errors"
	"time"
)

// ErrNoStartTime — время создания процесса на этой системе не узнать.
var ErrNoStartTime = errors.New("proc: время создания процесса недоступно")

// StartTime — когда создан процесс pid (UTC). Вместе с pid это личность
// процесса: pid переиспользуется, а время создания — нет. Так «жив ли наш
// исполнитель» отличают от «под этим pid уже чужой процесс» (урок crew:
// без времени создания на Linux живость оставалась «неизвестно»).
func StartTime(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, errors.New("proc: неположительный pid")
	}
	return startTime(pid)
}
