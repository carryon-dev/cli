package backend

// Session represents a running terminal session.
type Session struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Backend         string `json:"backend"`
	PID             int    `json:"pid,omitempty"`
	Created         int64  `json:"created"`
	LastAttached    int64  `json:"lastAttached,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	Command         string `json:"command,omitempty"`
	AttachedClients int    `json:"attachedClients"`
}

// CreateOpts contains options for creating a new session.
type CreateOpts struct {
	Name    string `json:"name,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Command string `json:"command,omitempty"`
	Shell   string `json:"shell,omitempty"`
	Backend string `json:"backend,omitempty"`
}

// FrameType identifies the type of a framed message.
type FrameType byte

const (
	FrameTerminalData    FrameType = 0x01
	FrameResize          FrameType = 0x02
	FrameStreamClose     FrameType = 0x03
	FrameResizeRequest   FrameType = 0x04
	FrameJsonRpc         FrameType = 0xFF
)
