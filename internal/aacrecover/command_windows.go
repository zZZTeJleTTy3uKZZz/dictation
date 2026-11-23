//go:build windows

package aacrecover

import (
	"context"
	"os/exec"

	"github.com/zZZTeJleTTy3uKZZz/dictation/internal/hidden"
)

// commandContext собирает вызов чужой программы так, чтобы не мигнуло окно
// консоли: служба диктовки живёт в фоне, и вспышка чёрного прямоугольника
// посреди разговора — это дефект, а не мелочь. Правило общее для всей
// программы, поэтому сборка вызова лежит в internal/hidden.
func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return hidden.CommandContext(ctx, name, args...)
}
