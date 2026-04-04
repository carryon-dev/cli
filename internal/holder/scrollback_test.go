package holder

import (
	"testing"
)

func TestScrollbackWriteAndRead(t *testing.T) {
	sb := NewScrollback(1024)
	sb.Write([]byte("hello "))
	sb.Write([]byte("world"))

	got := string(sb.Bytes())
	if got != "hello world" {
		t.Fatalf("expected %q, got %q", "hello world", got)
	}
}

func TestScrollbackEviction(t *testing.T) {
	sb := NewScrollback(10)
	sb.Write([]byte("aaaa")) // 4 bytes
	sb.Write([]byte("bbbb")) // 8 bytes total
	sb.Write([]byte("cccc")) // 12 bytes total, ring buffer keeps last 10

	got := string(sb.Bytes())
	// Ring buffer keeps the most recent 10 bytes of "aaaabbbbcccc"
	if got != "aabbbbcccc" {
		t.Fatalf("expected %q, got %q", "aabbbbcccc", got)
	}
	if sb.Len() != 10 {
		t.Fatalf("expected len 10, got %d", sb.Len())
	}
}

func TestScrollbackEvictionMultipleChunks(t *testing.T) {
	sb := NewScrollback(10)
	sb.Write([]byte("aa"))  // 2
	sb.Write([]byte("bb"))  // 4
	sb.Write([]byte("cc"))  // 6
	sb.Write([]byte("dd"))  // 8
	sb.Write([]byte("eee")) // 11 total, ring buffer keeps last 10

	got := string(sb.Bytes())
	// Ring buffer keeps the most recent 10 bytes of "aabbccddeee"
	if got != "abbccddeee" {
		t.Fatalf("expected %q, got %q", "abbccddeee", got)
	}
}

func TestScrollbackEmpty(t *testing.T) {
	sb := NewScrollback(1024)

	got := sb.Bytes()
	if len(got) != 0 {
		t.Fatalf("expected empty bytes, got %d bytes", len(got))
	}
	if sb.Len() != 0 {
		t.Fatalf("expected len 0, got %d", sb.Len())
	}
}

func TestScrollbackLen(t *testing.T) {
	sb := NewScrollback(1024)
	sb.Write([]byte("abc"))
	sb.Write([]byte("defgh"))

	if sb.Len() != 8 {
		t.Fatalf("expected len 8, got %d", sb.Len())
	}
}
