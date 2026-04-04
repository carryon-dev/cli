//go:build windows

package cmd

// watchResize is a no-op on Windows - Windows terminals handle resize
// differently (via console events, not signals). The terminal emulator
// (Windows Terminal, ConEmu, etc.) resizes the ConPTY directly.
func watchResize(onResize func()) func() {
	return func() {} // no-op cleanup
}
