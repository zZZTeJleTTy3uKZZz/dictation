//go:build !windows

package updates

import (
	"context"
	"fmt"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/selfupdate"
)

// runInstaller на не-Windows не ставит: установщик «Диктовки» — программа
// Windows. Скачивание и проверка манифеста здесь работают, установка — нет.
func runInstaller(ctx context.Context, path string, asset selfupdate.Asset, allUsers bool) error {
	return fmt.Errorf("%w: установщик Windows ставится только на Windows", selfupdate.ErrInstallFailed)
}
