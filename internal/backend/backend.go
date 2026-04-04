package backend

// Backend is the interface that terminal backends must implement.
type Backend interface {
	// ID returns the unique identifier for this backend (e.g. "native", "tmux").
	ID() string

	// Available reports whether this backend is usable on the current system.
	Available() bool

	// List returns all active sessions managed by this backend.
	List() []Session

	// Create starts a new terminal session with the given options.
	Create(opts CreateOpts) (Session, error)

	// Attach returns a StreamHandle for reading/writing to the session.
	Attach(sessionID string) (StreamHandle, error)

	// Resize changes the terminal dimensions of the given session.
	Resize(sessionID string, cols, rows uint16) error

	// Rename changes the display name of the given session.
	Rename(sessionID string, name string) error

	// GetScrollback returns the captured scrollback buffer as a string.
	GetScrollback(sessionID string) string

	// Kill terminates the given session.
	Kill(sessionID string) error

	// OnSessionCreated registers a listener called when a session is created.
	// Listeners are registered once at startup and never removed.
	OnSessionCreated(listener func(Session))

	// OnSessionEnded registers a listener called when a session ends.
	// The argument is the session ID.
	OnSessionEnded(listener func(string))

	// OnOutput registers a listener called when output is produced.
	// Arguments are sessionID and data.
	OnOutput(listener func(sessionID string, data []byte))

	// Shutdown terminates all sessions and releases resources.
	Shutdown()
}

// StreamHandle provides read/write access to an attached session.
type StreamHandle interface {
	// Write sends data to the session's terminal input.
	Write(data []byte) error

	// OnData registers a listener for incoming terminal output.
	// Returns a listener ID that can be passed to OffData.
	OnData(listener func(data []byte) int) int

	// OffData removes a previously registered data listener by ID.
	OffData(id int)

	// Close detaches from the session, removing all listeners.
	Close()
}
