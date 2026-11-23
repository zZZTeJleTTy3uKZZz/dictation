//go:build !linux && !windows && !darwin

package proc

import "time"

func startTime(int) (time.Time, error) { return time.Time{}, ErrNoStartTime }
