package remote_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/carryon-dev/cli/internal/remote"
)

func TestTransportListenAndDial(t *testing.T) {
	trA, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport A: %v", err)
	}
	defer trA.Close()

	trB, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport B: %v", err)
	}
	defer trB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Accept must run concurrently with Dial for the QUIC handshake to complete.
	type acceptResult struct {
		conn *quic.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		c, err := trB.Accept(ctx)
		acceptCh <- acceptResult{c, err}
	}()

	conn, err := trA.Dial(ctx, fmt.Sprintf("127.0.0.1:%d", trB.Port()))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseWithError(0, "test done")

	res := <-acceptCh
	if res.err != nil {
		t.Fatalf("Accept: %v", res.err)
	}
	accepted := res.conn
	defer accepted.CloseWithError(0, "test done")

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if _, err := stream.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	acceptedStream, err := accepted.AcceptStream(ctx)
	if err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}
	buf := make([]byte, 64)
	n, err := acceptedStream.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Fatalf("expected 'hello', got %q", buf[:n])
	}
}

func TestTransportLocalCandidates(t *testing.T) {
	tr, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	defer tr.Close()

	candidates := tr.LocalCandidates()
	if len(candidates) == 0 {
		t.Fatal("expected at least one local candidate")
	}

	for _, c := range candidates {
		if c.Type != "lan" {
			t.Errorf("expected type 'lan', got %q", c.Type)
		}
		if c.Port == 0 {
			t.Error("expected non-zero port")
		}
		if c.Addr == "" {
			t.Error("expected non-empty addr")
		}
	}
}

func TestTransport_DialUnreachable(t *testing.T) {
	tr, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	defer tr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = tr.Dial(ctx, "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error dialing unreachable address, got nil")
	}
}

func TestTransport_DialAfterClose(t *testing.T) {
	tr, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = tr.Dial(ctx, "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error dialing after close, got nil")
	}
}

func TestTransport_AcceptAfterClose(t *testing.T) {
	tr, err := remote.NewTransport(0)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = tr.Accept(ctx)
	if err == nil {
		t.Fatal("expected error accepting after close, got nil")
	}
}

func TestTransport_Addr(t *testing.T) {
	tr, err := remote.NewTransport(0)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	addr := tr.Addr()
	if addr == nil {
		t.Fatal("expected non-nil address")
	}
	if addr.String() == "" {
		t.Fatal("expected non-empty address string")
	}
}

func TestTransport_STUNDiscoverBadServer(t *testing.T) {
	tr, err := remote.NewTransport(0)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Invalid server address - no host:port format - should fail to resolve.
	_, err = tr.STUNDiscover(ctx, "not-a-valid-address")
	if err == nil {
		t.Fatal("expected error with invalid STUN server")
	}
}

func TestTransport_STUNDiscoverTimeout(t *testing.T) {
	tr, err := remote.NewTransport(0)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	// Use a valid address format but one that won't respond (localhost port 1).
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err = tr.STUNDiscover(ctx, "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
