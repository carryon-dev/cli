//go:build !windows

package pty_test

import (
	"io"
	"testing"
	"time"

	"github.com/carryon-dev/cli/internal/pty"
)

func TestSpawnAndRead(t *testing.T) {
	p, err := pty.Spawn("/bin/echo", []string{"hello-pty"}, pty.SpawnOpts{
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer p.Close()

	done := make(chan string, 1)
	go func() {
		var buf []byte
		tmp := make([]byte, 1024)
		deadline := time.After(3 * time.Second)
		for {
			select {
			case <-deadline:
				done <- string(buf)
				return
			default:
			}
			n, err := p.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err != nil {
				done <- string(buf)
				return
			}
		}
	}()

	select {
	case output := <-done:
		if len(output) == 0 {
			t.Fatal("expected output from echo, got nothing")
		}
		if !containsString(output, "hello-pty") {
			t.Fatalf("expected output to contain 'hello-pty', got %q", output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for output")
	}
}

func TestSpawnAndWrite(t *testing.T) {
	p, err := pty.Spawn("/bin/cat", nil, pty.SpawnOpts{
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer p.Close()

	_, err = p.Write([]byte("test-input\n"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	done := make(chan string, 1)
	go func() {
		var buf []byte
		tmp := make([]byte, 1024)
		deadline := time.After(3 * time.Second)
		for {
			select {
			case <-deadline:
				done <- string(buf)
				return
			default:
			}
			n, err := p.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				// Once we have enough data, check if it contains our input
				if containsString(string(buf), "test-input") {
					done <- string(buf)
					return
				}
			}
			if err == io.EOF {
				done <- string(buf)
				return
			}
		}
	}()

	select {
	case output := <-done:
		if !containsString(output, "test-input") {
			t.Fatalf("expected output to contain 'test-input', got %q", output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for output")
	}
}

func TestResize(t *testing.T) {
	p, err := pty.Spawn("/bin/sh", nil, pty.SpawnOpts{
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer p.Close()

	if err := p.Resize(120, 40); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}
}

func TestPid(t *testing.T) {
	p, err := pty.Spawn("/bin/sh", nil, pty.SpawnOpts{
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer p.Close()

	pid := p.Pid()
	if pid <= 0 {
		t.Fatalf("expected Pid() > 0, got %d", pid)
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
