//go:build !windows

package cmd

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize listens for SIGWINCH and calls the callback on each resize.
// Returns a cleanup function that stops watching.
func watchResize(onResize func()) func() {
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)
	go func() {
		for range sigwinch {
			onResize()
		}
	}()
	return func() {
		signal.Stop(sigwinch)
		close(sigwinch)
	}
}
