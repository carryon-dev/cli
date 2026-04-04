//go:build !windows

package pty

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	creackpty "github.com/creack/pty"
)

// unixPty implements the Pty interface using creack/pty on Unix systems.
type unixPty struct {
	file      *os.File
	cmd       *exec.Cmd
	waitOnce  sync.Once
	waitErr   error
	closeOnce sync.Once
	closeErr  error
}

// Spawn creates a new PTY running the given shell command.
func Spawn(shell string, args []string, opts SpawnOpts) (Pty, error) {
	cmd := exec.Command(shell, args...)

	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	if len(opts.Env) > 0 {
		cmd.Env = opts.Env
	}

	cols := opts.Cols
	if cols == 0 {
		cols = 80
	}
	rows := opts.Rows
	if rows == 0 {
		rows = 24
	}

	f, err := creackpty.StartWithSize(cmd, &creackpty.Winsize{
		Cols: cols,
		Rows: rows,
	})
	if err != nil {
		return nil, err
	}

	return &unixPty{
		file: f,
		cmd:  cmd,
	}, nil
}

func (p *unixPty) Read(buf []byte) (int, error) {
	return p.file.Read(buf)
}

func (p *unixPty) Write(buf []byte) (int, error) {
	return p.file.Write(buf)
}

func (p *unixPty) Resize(cols, rows uint16) error {
	ws := struct {
		Rows uint16
		Cols uint16
		X    uint16
		Y    uint16
	}{
		Rows: rows,
		Cols: cols,
	}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		p.file.Fd(),
		syscall.TIOCSWINSZ,
		uintptr(unsafe.Pointer(&ws)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func (p *unixPty) Pid() int {
	if p.cmd.Process == nil {
		return -1
	}
	return p.cmd.Process.Pid
}

func (p *unixPty) wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
	})
	return p.waitErr
}

func (p *unixPty) Wait() error {
	return p.wait()
}

func (p *unixPty) Close() error {
	p.closeOnce.Do(func() {
		// Close the PTY file descriptor first. This will cause reads to return
		// EOF and signals to the child that the terminal is gone.
		p.closeErr = p.file.Close()

		if p.cmd.Process != nil {
			// Send SIGTERM to the process, ignoring errors (process may have
			// already exited).
			_ = p.cmd.Process.Kill()
			// Wait to reap the zombie process. Use the shared wait() helper so
			// concurrent calls to Wait() and Close() do not both call cmd.Wait().
			_ = p.wait()
		}
	})
	return p.closeErr
}
