//go:build !windows

package supervisor

import (
	"context"
	"io"
	"os/exec"
)

func runContained(ctx context.Context, spec commandSpec, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, spec.Path, spec.Args...)
	command.Env = spec.Env
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}
