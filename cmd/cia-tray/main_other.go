//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stdout, stderr := defaultStreams()
	if err := run(ctx, os.Args[1:], stdout, stderr); err != nil {
		_, _ = fmt.Fprintln(stderr, "cia-tray:", err)
		os.Exit(1)
	}
}
