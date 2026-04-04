//go:build windows

package pty

import (
	"context"
	"sync"

	"github.com/UserExistsError/conpty"
)

// windowsPty implements the Pty interface using Windows ConPTY.
type windowsPty struct {
	cpty     *conpty.ConPty
	closeOnce sync.Once
	closeErr  error
}

// Spawn creates a new PTY running the given shell command using ConPTY.
func Spawn(shell string, args []string, opts SpawnOpts) (Pty, error) {
	// Build command line string: shell + args joined with spaces.
	cmdLine := shell
	for _, a := range args {
		cmdLine += " " + a
	}

	cols := opts.Cols
	if cols == 0 {
		cols = 80
	}
	rows := opts.Rows
	if rows == 0 {
		rows = 24
	}

	var options []conpty.ConPtyOption
	options = append(options, conpty.ConPtyDimensions(int(cols), int(rows)))

	if opts.Cwd != "" {
		options = append(options, conpty.ConPtyWorkDir(opts.Cwd))
	}
	if len(opts.Env) > 0 {
		options = append(options, conpty.ConPtyEnv(opts.Env))
	}

	cpty, err := conpty.Start(cmdLine, options...)
	if err != nil {
		return nil, err
	}
	return &windowsPty{cpty: cpty}, nil
}

func (p *windowsPty) Read(buf []byte) (int, error) {
	return p.cpty.Read(buf)
}

func (p *windowsPty) Write(data []byte) (int, error) {
	return p.cpty.Write(data)
}

func (p *windowsPty) Resize(cols, rows uint16) error {
	return p.cpty.Resize(int(cols), int(rows))
}

func (p *windowsPty) Pid() int {
	return p.cpty.Pid()
}

func (p *windowsPty) Wait() error {
	_, err := p.cpty.Wait(context.Background())
	return err
}

func (p *windowsPty) Close() error {
	p.closeOnce.Do(func() {
		p.closeErr = p.cpty.Close()
	})
	return p.closeErr
}
