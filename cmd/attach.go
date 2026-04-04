package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/holder"
	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <session>",
		Short: "Attach to an existing session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(client *ipc.Client) error {
				sessionID, err := resolveSession(client, args[0])
				if err != nil {
					return err
				}
				return interactiveAttach(client, sessionID)
			})
		},
	}
}

// interactiveAttach enters raw mode, connects directly to the holder socket
// for local sessions, and pipes stdin/stdout with double Ctrl+C detach.
// Remote sessions are delegated to interactiveAttachRemote.
func interactiveAttach(client *ipc.Client, sessionID string) error {
	// Compute socket path for fire-and-forget daemon notifications.
	socketPath := daemon.GetSocketPath(daemon.GetBaseDir())

	// Identify this client to the daemon.
	clientType := os.Getenv("CARRYON_CLIENT_TYPE")
	if clientType == "" {
		clientType = "cli"
	}
	clientName := os.Getenv("CARRYON_CLIENT_NAME")
	if clientName == "" {
		clientName = "Terminal"
	}
	client.Call("client.identify", map[string]any{
		"type": clientType,
		"name": clientName,
		"pid":  float64(os.Getpid()),
	})

	// Call session.attach to get routing info.
	result, err := client.Call("session.attach", map[string]any{"sessionId": sessionID})
	if err != nil {
		return fmt.Errorf("failed to attach: %w", err)
	}

	rm, ok := result.(map[string]any)
	if !ok {
		return fmt.Errorf("unexpected response from session.attach")
	}

	// Remote sessions use the daemon-stream path.
	if _, isRemote := rm["remote"]; isRemote {
		return interactiveAttachRemote(client, sessionID)
	}

	// Local session - connect directly to holder socket.
	holderSocket, _ := rm["holderSocket"].(string)
	if holderSocket == "" {
		return fmt.Errorf("session.attach did not return holderSocket")
	}

	hc, scrollback, err := holder.ConnectHolder(holderSocket)
	if err != nil {
		return fmt.Errorf("connect to holder: %w", err)
	}

	// Notify daemon that we attached (fire-and-forget).
	go notifyDaemon(socketPath, "session.attached", map[string]any{"sessionId": sessionID})

	// Enter raw mode.
	stdinFd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		hc.Close()
		return fmt.Errorf("failed to enter raw mode: %w", err)
	}

	cleanup := func() {
		hc.Close()
		term.Restore(stdinFd, oldState)
	}

	// Clear screen and write scrollback.
	os.Stdout.Write([]byte("\033[2J\033[H"))
	if len(scrollback) > 0 {
		os.Stdout.Write(scrollback)
	}

	// Forward holder output to stdout.
	hc.OnData(func(data []byte) {
		os.Stdout.Write(data)
	})

	// Listen for shell exit from the holder.
	done := make(chan struct{}, 1)
	hc.OnExit(func(code int32) {
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Respond to resize requests from the holder (another client resized).
	hc.OnResizeRequest(func() {
		sendResizeToHolder(hc)
	})

	// Also listen for session.ended from daemon (e.g. kill).
	client.OnNotification("session.ended", func(params map[string]any) {
		if endedID, _ := params["sessionId"].(string); endedID == sessionID {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	// Send initial resize directly to holder.
	sendResizeToHolder(hc)

	// Watch for terminal resize - send directly to holder.
	stopResize := watchResize(func() {
		sendResizeToHolder(hc)
	})

	// Read stdin and forward to holder; track double Ctrl+C.
	ctrlCCount := 0
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := os.Stdin.Read(buf)
			if n > 0 {
				data := buf[:n]
				if err := hc.Write(data); err != nil {
					select {
					case done <- struct{}{}:
					default:
					}
					return
				}

				if n == 1 && data[0] == 0x03 {
					ctrlCCount++
					if ctrlCCount >= 2 {
						select {
						case done <- struct{}{}:
						default:
						}
						return
					}
				} else {
					ctrlCCount = 0
				}
			}
			if readErr != nil {
				select {
				case done <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	<-done
	stopResize()
	cleanup()

	// Fire-and-forget: notify daemon of detach.
	go notifyDaemon(socketPath, "session.detached", map[string]any{"sessionId": sessionID})

	fmt.Fprintln(os.Stderr, "\r\n[local] session ended")
	return nil
}

// interactiveAttachRemote handles remote session attachment using the
// daemon-stream path (existing behavior for remote sessions).
func interactiveAttachRemote(client *ipc.Client, sessionID string) error {
	// Set up stream buffer BEFORE attach so scrollback frames are captured.
	stream := client.AttachStream(sessionID)

	// Enter raw mode.
	stdinFd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		stream.Close()
		return fmt.Errorf("failed to enter raw mode: %w", err)
	}

	cleanup := func() {
		stream.Close()
		term.Restore(stdinFd, oldState)
	}

	// Clear screen before writing scrollback.
	os.Stdout.Write([]byte("\033[2J\033[H"))

	// Forward stream data to stdout.
	stream.OnData(func(data []byte) {
		os.Stdout.Write(data)
	})

	// Listen for session ending.
	done := make(chan struct{}, 1)
	client.OnNotification("session.ended", func(params map[string]any) {
		if endedID, _ := params["sessionId"].(string); endedID == sessionID {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	// Respond to resize requests from the daemon (another client resized).
	client.OnResizeRequest(func(sid string) {
		if sid == sessionID {
			sendResize(client, sessionID)
		}
	})

	// Send initial resize via daemon (remote path).
	sendResize(client, sessionID)

	// Watch for terminal resize (send via daemon for remote).
	stopResize := watchResize(func() {
		sendResize(client, sessionID)
	})

	// Read stdin and forward to stream; track double Ctrl+C.
	ctrlCCount := 0
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := os.Stdin.Read(buf)
			if n > 0 {
				data := buf[:n]
				if err := stream.Write(data); err != nil {
					select {
					case done <- struct{}{}:
					default:
					}
					return
				}

				if n == 1 && data[0] == 0x03 {
					ctrlCCount++
					if ctrlCCount >= 2 {
						select {
						case done <- struct{}{}:
						default:
						}
						return
					}
				} else {
					ctrlCCount = 0
				}
			}
			if readErr != nil {
				select {
				case done <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	<-done
	stopResize()
	cleanup()
	fmt.Fprintln(os.Stderr, "\r\n[remote] session ended")
	return nil
}

// notifyDaemon opens a fresh connection to the daemon, sends a fire-and-forget
// RPC call, and disconnects. If the daemon is unavailable the call is silently
// skipped. The entire operation is capped at 500ms.
func notifyDaemon(socketPath string, method string, params map[string]any) {
	done := make(chan struct{}, 1)
	go func() {
		defer func() { done <- struct{}{} }()
		c := ipc.NewClient()
		if err := c.Connect(socketPath); err != nil {
			return // daemon unavailable, silently skip
		}
		defer c.Disconnect()
		c.Call(method, params)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		// timeout, silently skip
	}
}

// sendResizeToHolder sends the current terminal size directly to the holder.
func sendResizeToHolder(hc *holder.HolderClient) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err == nil && w > 0 && h > 0 {
		hc.Resize(uint16(w), uint16(h))
	}
}

// sendResize sends the current terminal size to the daemon (for remote sessions).
func sendResize(client *ipc.Client, sessionID string) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err == nil && w > 0 && h > 0 {
		client.SendResize(sessionID, w, h)
	}
}
