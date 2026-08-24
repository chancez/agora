package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// os.Exit skips deferred calls, so the whole program runs inside run().
	os.Exit(run())
}

func run() int {
	// agora watch blocks until a signal, and a signal is how it is meant to end: not an error.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := &app{in: os.Stdin, out: os.Stdout, errOut: os.Stderr}
	defer a.close()
	return execute(ctx, a, os.Args[1:])
}

// execute runs one command and returns the process exit status. Tests drive this directly, so the
// mapping from an error to a status is the same one a caller sees.
func execute(ctx context.Context, a *app, args []string) int {
	root := newRootCmd(a)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var code exitCode
	if errors.As(err, &code) {
		// The command already printed everything the caller needs: a lost claim has named its
		// holder, which is the output that decides whether work gets duplicated.
		return int(code)
	}
	fmt.Fprintf(a.errOut, "agora: %v\n", err)
	return 1
}
