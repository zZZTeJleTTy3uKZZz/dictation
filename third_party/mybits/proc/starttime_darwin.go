//go:build darwin

package proc

import (
	"time"

	"golang.org/x/sys/unix"
)

func startTime(pid int) (time.Time, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, err
	}
	tv := kp.Proc.P_starttime
	return time.Unix(tv.Sec, int64(tv.Usec)*1000).UTC(), nil
}
