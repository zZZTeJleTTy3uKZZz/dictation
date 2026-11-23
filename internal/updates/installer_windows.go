//go:build windows

package updates

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/proc"
	"github.com/zZZTeJleTTy3uKZZz/dictation/third_party/mybits/selfupdate"
)

// runInstaller запускает скачанный установщик тихо, скрытым окном (§5):
// видимых окон на машине человека не появляется. allUsers — установка «для всех»
// (служба Windows): тогда установщику передаётся /ALLUSERS, иначе обновление
// поставило бы вторую копию для пользователя.
func runInstaller(ctx context.Context, path string, asset selfupdate.Asset, allUsers bool) error {
	program, args, err := installerCommand(path, asset, allUsers)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(program); statErr != nil && program == path {
		return fmt.Errorf("%w: установщик %s: %v", selfupdate.ErrInstallFailed, path, statErr)
	}
	cmd := exec.CommandContext(ctx, program, args...)
	proc.Hide(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code := exit.ExitCode()
			// 3 — установщик отказался ставить: идёт запись.
			// Код 3 в окно обновления не превращаем в «повторить»: причина не
			// в сети, а в том, что человек диктует; установку отложит служба.
			return fmt.Errorf("%w: код %d: %s", selfupdate.ErrInstallFailed, code, tail(out))
		}
		return fmt.Errorf("%w: %v", selfupdate.ErrInstallFailed, err)
	}
	return nil
}

// installerCommand — программа и тихие параметры установщика (§5). Свои args
// манифеста заменяют умолчание целиком.
func installerCommand(path string, asset selfupdate.Asset, allUsers bool) (string, []string, error) {
	if len(asset.Args) > 0 {
		if asset.Installer == selfupdate.InstallerMSI {
			return "msiexec.exe", append([]string(nil), asset.Args...), nil
		}
		return path, append([]string(nil), asset.Args...), nil
	}
	switch asset.Installer {
	case selfupdate.InstallerInno:
		scope := "/CURRENTUSER"
		if allUsers {
			scope = "/ALLUSERS"
		}
		return path, []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-", scope}, nil
	case selfupdate.InstallerMSI:
		return "msiexec.exe", []string{"/i", path, "/qn", "/norestart"}, nil
	case selfupdate.InstallerNSIS:
		return path, []string{"/S"}, nil
	}
	return "", nil, fmt.Errorf("%w: установщик %q", selfupdate.ErrBadManifest, asset.Installer)
}

// tail — последние строки вывода установщика для причины отказа; весь журнал
// установщика пишется им самим в файл.
func tail(out []byte) string {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "без вывода"
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, " | ")
}
