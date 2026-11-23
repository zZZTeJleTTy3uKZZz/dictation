//go:build !windows

package proc

import "os/exec"

// Hide вне Windows не меняет свойства процесса.
func Hide(*exec.Cmd) {}
