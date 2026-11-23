//go:build !windows

package selfupdate

import (
	"context"
	"fmt"
)

// checkInstallerSupport — kind: installer есть только на Windows (§5).
func checkInstallerSupport() error {
	return fmt.Errorf("%w: kind: installer — только Windows", ErrUnsupported)
}

// runInstaller на прочих системах не запускает ничего.
func runInstaller(context.Context, string, Asset) error {
	return checkInstallerSupport()
}
