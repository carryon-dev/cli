package holder

import "sync"

// Scrollback is a fixed-size ring buffer that stores the most recent terminal
// output. A single pre-allocated byte slice avoids per-write allocations.
// All methods are safe for concurrent use.
type Scrollback struct {
	mu   sync.RWMutex
	buf  []byte
	head int  // write position (wraps around)
	used int  // number of valid bytes in the buffer
	cap  int
}

// NewScrollback creates a new Scrollback with the given maximum byte capacity.
func NewScrollback(maxBytes int) *Scrollback {
	return &Scrollback{
		buf: make([]byte, maxBytes),
		cap: maxBytes,
	}
}

// Write appends data to the scrollback buffer, overwriting the oldest data
// if the buffer is full. No per-call allocations.
func (s *Scrollback) Write(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := len(data)
	if n == 0 {
		return
	}

	// If data is larger than the buffer, only keep the tail.
	if n >= s.cap {
		copy(s.buf, data[n-s.cap:])
		s.head = 0
		s.used = s.cap
		return
	}

	// Write in up to two segments (wrap around).
	first := s.cap - s.head
	if first >= n {
		copy(s.buf[s.head:], data)
	} else {
		copy(s.buf[s.head:], data[:first])
		copy(s.buf, data[first:])
	}

	s.head = (s.head + n) % s.cap
	s.used += n
	if s.used > s.cap {
		s.used = s.cap
	}
}

// Bytes returns the contents of the buffer in order (oldest to newest).
func (s *Scrollback) Bytes() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.used == 0 {
		return nil
	}

	out := make([]byte, s.used)
	if s.used < s.cap {
		// Buffer hasn't wrapped yet - data starts at 0.
		copy(out, s.buf[:s.used])
	} else {
		// Buffer has wrapped - oldest data starts at head.
		first := s.cap - s.head
		copy(out, s.buf[s.head:])
		copy(out[first:], s.buf[:s.head])
	}
	return out
}

// Len returns the total number of bytes in the buffer.
func (s *Scrollback) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.used
}
