package holder

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// SpawnProcess launches a holder as a detached child process by re-executing
// the current binary with the __holder subcommand. It returns the socket path
// and PID of the new holder process.
func SpawnProcess(opts SpawnOpts) (sockPath string, holderPID int, err error) {
	exe := opts.Executable
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			return "", 0, fmt.Errorf("resolve executable: %w", err)
		}
	}

	sockPath = SocketPath(opts.BaseDir, opts.SessionID)

	args := []string{
		"__holder",
		"--session-id", opts.SessionID,
		"--shell", opts.Shell,
		"--cwd", opts.Cwd,
		"--command", opts.Command,
		"--cols", strconv.Itoa(int(opts.Cols)),
		"--rows", strconv.Itoa(int(opts.Rows)),
		"--base-dir", opts.BaseDir,
	}

	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = detachAttr()
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	cmd.Env = opts.Env
	if len(cmd.Env) == 0 {
		cmd.Env = os.Environ()
	}

	if err := cmd.Start(); err != nil {
		return "", 0, fmt.Errorf("start holder process: %w", err)
	}

	pid := cmd.Process.Pid
	_ = cmd.Process.Release()

	// Poll for socket readiness with exponential backoff.
	timeout := time.After(5 * time.Second)
	delay := 50 * time.Millisecond
	for {
		select {
		case <-timeout:
			return "", 0, fmt.Errorf("holder socket not ready after 5s: %s", sockPath)
		default:
		}
		time.Sleep(delay)
		conn, err := Dial(sockPath)
		if err == nil {
			conn.Close()
			return sockPath, pid, nil
		}
		if delay < 500*time.Millisecond {
			delay *= 2
		}
	}
}
