package pty

// Pty is the cross-platform interface for pseudo-terminal operations.
type Pty interface {
	// Read reads from the PTY master file descriptor.
	Read(p []byte) (n int, err error)

	// Write writes to the PTY master file descriptor.
	Write(p []byte) (n int, err error)

	// Resize changes the terminal dimensions.
	Resize(cols, rows uint16) error

	// Pid returns the process ID of the child process.
	Pid() int

	// Wait blocks until the child process exits and returns nil.
	// On Unix, this calls cmd.Wait(). On Windows, this calls conpty.Wait().
	// This is needed because on Windows, Read() may not return EOF when the
	// process exits - the conpty output pipe can stay open indefinitely.
	Wait() error

	// Close closes the PTY and terminates the child process.
	Close() error
}

// SpawnOpts configures the spawned PTY session.
type SpawnOpts struct {
	Cols uint16
	Rows uint16
	Cwd  string
	Env  []string
}
