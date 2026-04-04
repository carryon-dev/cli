package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/config"
	"github.com/carryon-dev/cli/internal/crypto"
	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/carryon-dev/cli/internal/logging"
	"github.com/carryon-dev/cli/internal/remote"
	"github.com/carryon-dev/cli/internal/session"
)

// RemoteSubsystem manages the signaling connection, remote state, and P2P/relay
// bridges. It implements ipc.RemoteService.
type RemoteSubsystem struct {
	mu         sync.Mutex
	client     *remote.SignalingClient
	handlers   *SignalingHandlers
	states     sync.Map // teamID -> *remote.RemoteState
	connecting bool

	transport      *remote.Transport
	creds          *remote.Credentials
	remotePath     string
	cfg            *config.Manager
	logger         *logging.Logger
	sessionManager *session.Manager
	broadcastFn    func(string, map[string]any)
	daemonCtx      context.Context

}

// RemoteOpts holds the parameters for NewRemote.
type RemoteOpts struct {
	Creds          *remote.Credentials
	RemotePath     string
	Config         *config.Manager
	Logger         *logging.Logger
	SessionManager *session.Manager
	DaemonCtx      context.Context
	BroadcastFn    func(string, map[string]any)
}

// NewRemote creates a RemoteSubsystem, starts the QUIC transport,
// and wires session change listeners to publish sessions on changes.
func NewRemote(opts RemoteOpts) *RemoteSubsystem {
	rs := &RemoteSubsystem{
		creds:          opts.Creds,
		remotePath:     opts.RemotePath,
		cfg:            opts.Config,
		logger:         opts.Logger,
		sessionManager: opts.SessionManager,
		broadcastFn:    opts.BroadcastFn,
		daemonCtx:      opts.DaemonCtx,
	}

	// Initialize QUIC transport for P2P connections.
	quicPort := 4900
	qt, err := remote.NewTransport(quicPort)
	if err != nil {
		rs.logger.Warn("remote", fmt.Sprintf("Failed to start QUIC transport on port %d, P2P disabled: %v", quicPort, err))
	} else {
		rs.logger.Info("remote", fmt.Sprintf("QUIC transport listening on port %d", qt.Port()))
		rs.transport = qt
	}

	// Re-publish sessions when local state changes.
	rs.sessionManager.OnSessionCreated(func(_ backend.Session) { go rs.PublishSessions() })
	rs.sessionManager.OnSessionEnded(func(_ string) { go rs.PublishSessions() })
	rs.sessionManager.OnSessionRenamed(func(_, _ string) { go rs.PublishSessions() })

	rs.logger.Info("remote", fmt.Sprintf("Device '%s' configured (account %s)", rs.creds.DeviceName, rs.creds.AccountID))

	return rs
}

// Status returns the current remote connection status.
func (rs *RemoteSubsystem) Status() map[string]any {
	rs.mu.Lock()
	sc := rs.client
	rs.mu.Unlock()
	// Check both that the client exists and that its read loop is still alive.
	connected := sc != nil
	if connected {
		select {
		case <-sc.Done():
			connected = false
		default:
		}
	}
	result := map[string]any{
		"connected":   connected,
		"account_id":  rs.creds.AccountID,
		"device_id":   rs.creds.DeviceID,
		"device_name": rs.creds.DeviceName,
	}
	return result
}

// PublishSessions re-publishes local sessions to the signaling service so
// other devices can see them.
func (rs *RemoteSubsystem) PublishSessions() {
	rs.mu.Lock()
	sc := rs.client
	rs.mu.Unlock()
	if sc == nil {
		return
	}
	_, priv, err := crypto.LoadKeypair(rs.remotePath)
	if err != nil {
		return
	}

	var recipients map[string][]byte
	if val, ok := rs.states.Load(rs.creds.TeamID); ok {
		state := val.(*remote.RemoteState)
		recipients = state.Recipients()
	}
	if len(recipients) == 0 {
		return
	}

	localSessions := rs.sessionManager.List()
	blob, err := remote.BuildSessionBlob(localSessions, rs.creds.DeviceID, rs.creds.DeviceName, priv, recipients)
	if err != nil || blob == "" {
		return
	}
	_ = sc.Send(context.Background(), "sessions.update", remote.SessionsUpdateMsg{EncryptedBlob: blob})
}

// Connect initiates a connection to the signaling service. The actual connection
// runs asynchronously so the caller returns immediately.
func (rs *RemoteSubsystem) Connect() error {
	rs.mu.Lock()
	if rs.client != nil {
		rs.mu.Unlock()
		return nil
	}
	if rs.connecting {
		rs.mu.Unlock()
		return fmt.Errorf("connect already in progress")
	}
	rs.connecting = true
	rs.mu.Unlock()

	// Run the connect asynchronously so the IPC handler returns immediately.
	go func() {
		defer func() {
			rs.mu.Lock()
			rs.connecting = false
			rs.mu.Unlock()
		}()

		pub, priv, loadErr := crypto.LoadKeypair(rs.remotePath)
		if loadErr != nil {
			rs.logger.Warn("remote", fmt.Sprintf("Connect failed (keypair): %v", loadErr))
			if rs.broadcastFn != nil {
				rs.broadcastFn("remote.updated", map[string]any{"error": loadErr.Error()})
			}
			return
		}

		client := remote.NewSignalingClient(
			SignalingURL(),
			rs.creds.DeviceID,
			rs.creds.DeviceName,
			rs.creds.TeamID,
			rs.creds.SessionToken,
			pub,
		)

		skipTLS := !rs.cfg.GetBool("remote.relay_tls_verify")

		h := &SignalingHandlers{
			Logger:       rs.logger,
			Ctx:          rs.daemonCtx,
			DeviceID:     rs.creds.DeviceID,
			DeviceName:   rs.creds.DeviceName,
			TeamID:       rs.creds.TeamID,
			TeamName:     rs.creds.TeamName,
			PrivateKey:   priv,
			RemoteStates: &rs.states,
			BroadcastFn:  rs.broadcastFn,
			ListSessions: func() []backend.Session {
				return rs.sessionManager.List()
			},
			SendMessage: func(msgType string, payload any) error {
				return client.Send(context.Background(), msgType, payload)
			},
			Transport:    rs.transport,
			SkipRelayTLS: skipTLS,
		}
		h.connectSem = make(chan struct{}, 10)
		h.CreateSession = func(opts backend.CreateOpts) (backend.Session, error) {
			return rs.sessionManager.Create(opts)
		}
		h.AttachSession = func(sessionID string) (backend.StreamHandle, error) {
			return rs.sessionManager.Attach(sessionID)
		}

		// Wire PublishSessions before Connect so it is available when the
		// signaling read loop fires initial.state or device.presence immediately
		// on connect.
		h.PublishSessions = rs.PublishSessions

		h.SaveToken = func(newToken string) {
			rs.mu.Lock()
			rs.creds.SessionToken = newToken
			rs.mu.Unlock()
			if err := remote.SaveCredentials(rs.remotePath, rs.creds); err != nil {
				rs.logger.Warn("remote", fmt.Sprintf("Failed to save rotated token: %v", err))
			} else {
				rs.logger.Info("remote", "Session token rotated")
			}
		}

		client.OnMessage("initial.state", h.HandleInitialState)
		client.OnMessage("sessions.updated", h.HandleSessionsUpdated)
		client.OnMessage("device.presence", h.HandleDevicePresence)
		client.OnMessage("recipients.update", h.HandleRecipientsUpdate)
		client.OnMessage("session.create.request", h.HandleSessionCreateRequest)
		client.OnMessage("session.create.response", h.HandleSessionCreateResponse)
		client.OnMessage("connect.offer", h.HandleConnectOffer)
		client.OnMessage("connect.answer", h.HandleConnectAnswer)

		// Connect does network I/O - do not hold the lock during this call.
		if err := client.Connect(context.Background()); err != nil {
			rs.logger.Warn("remote", fmt.Sprintf("Connect failed: %v", err))
			if rs.broadcastFn != nil {
				rs.broadcastFn("remote.updated", map[string]any{"error": err.Error()})
			}
			return
		}

		rs.mu.Lock()
		rs.client = client
		rs.handlers = h
		rs.mu.Unlock()

		rs.logger.Info("remote", "Connected to signaling service")

		if rs.broadcastFn != nil {
			rs.broadcastFn("remote.updated", nil)
		}

		// Monitor the signaling connection and clean up on disconnect
		// so Status reflects the actual state.
		go func() {
			<-client.Done()
			closeErr := client.CloseError()
			rs.mu.Lock()
			if rs.client == client {
				rs.client = nil
				rs.handlers = nil
			}
			rs.mu.Unlock()
			if closeErr != nil {
				rs.logger.Warn("remote", fmt.Sprintf("Signaling connection lost: %v", closeErr))
			} else {
				rs.logger.Warn("remote", "Signaling connection lost")
			}
			if rs.broadcastFn != nil {
				rs.broadcastFn("remote.disconnected", nil)
			}
		}()
	}()

	return nil
}

// Disconnect closes the signaling connection.
func (rs *RemoteSubsystem) Disconnect() {
	rs.mu.Lock()
	sc := rs.client
	rs.client = nil
	rs.handlers = nil
	rs.mu.Unlock()
	if sc != nil {
		sc.Close()
		rs.logger.Info("remote", "Disconnected from signaling service")
	}
}

// CreateSession asks a remote device to create a new session.
func (rs *RemoteSubsystem) CreateSession(deviceID string, opts backend.CreateOpts) (string, error) {
	rs.mu.Lock()
	sc := rs.client
	h := rs.handlers
	rs.mu.Unlock()
	if sc == nil {
		return "", fmt.Errorf("not connected to signaling")
	}

	requestID := fmt.Sprintf("create-%d", time.Now().UnixNano())

	// Register a per-request response channel. This survives reconnects
	// because it lives on the handler's sync.Map, not a per-connection channel.
	respCh := make(chan remote.SessionCreateResponseMsg, 1)
	h.createResponseChannels.Store(requestID, respCh)
	defer h.createResponseChannels.Delete(requestID)

	err := sc.Send(context.Background(), "session.create.request", remote.SessionCreateRequestMsg{
		RequestID:      requestID,
		TargetDeviceID: deviceID,
		Name:           opts.Name,
		Cwd:            opts.Cwd,
		Command:        opts.Command,
	})
	if err != nil {
		return "", fmt.Errorf("send create request: %w", err)
	}

	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()

	select {
	case resp := <-respCh:
		if resp.Error != "" {
			return "", fmt.Errorf("remote create failed: %s", resp.Error)
		}
		return resp.SessionID, nil
	case <-timer.C:
		return "", fmt.Errorf("remote create timed out")
	}
}

// AttachSession connects an IPC client to a remote session via P2P or relay.
func (rs *RemoteSubsystem) AttachSession(client *ipc.ClientState, sessionID string, rCtx *ipc.RpcContext) error {
	rs.mu.Lock()
	sc := rs.client
	h := rs.handlers
	rs.mu.Unlock()
	if sc == nil {
		return fmt.Errorf("not connected to signaling")
	}

	// Serialize connect flows so two concurrent callers cannot race on
	// ConnectAnswerCh (the signaling protocol does not include routing
	// info in connect.answer, so per-request dispatch is not possible).
	h.connectMu.Lock()
	defer h.connectMu.Unlock()

	// Find which device owns this session
	var targetDeviceID string
	rs.states.Range(func(_, val any) bool {
		state := val.(*remote.RemoteState)
		for _, dev := range state.Devices() {
			for _, sess := range state.Sessions(dev.ID) {
				if sess.ID == sessionID {
					targetDeviceID = dev.ID
					return false
				}
			}
		}
		return true
	})

	if targetDeviceID == "" {
		return fmt.Errorf("session %s not found on any remote device", sessionID)
	}

	// Generate ephemeral keypair
	ephPub, ephPriv, err := crypto.GenerateKeypair()
	if err != nil {
		return fmt.Errorf("generate ephemeral keypair: %w", err)
	}

	// Gather our own P2P candidates
	var ourCandidates []remote.Candidate
	if rs.transport != nil {
		ourCandidates = rs.transport.LocalCandidates()
	}

	// STUN discovery - extend candidates with public address if reachable
	if rs.transport != nil {
		stunCtx, stunCancel := context.WithTimeout(context.Background(), 3*time.Second)
		stunCandidate, err := rs.transport.STUNDiscover(stunCtx, "stun.l.google.com:19302")
		stunCancel()
		if err != nil {
			rs.logger.Info("remote", fmt.Sprintf("STUN discovery failed (P2P may still work via LAN): %v", err))
		} else {
			ourCandidates = append(ourCandidates, stunCandidate)
		}
	}

	requestID := fmt.Sprintf("connect-%d", time.Now().UnixNano())

	// Register a per-request answer channel so HandleConnectAnswer
	// dispatches directly to us without a shared channel.
	answerCh := h.RegisterAnswerChannel(requestID)
	defer h.UnregisterAnswerChannel(requestID)

	// Send connect.request
	err = sc.Send(context.Background(), "connect.request", remote.ConnectRequestMsg{
		RequestID:       requestID,
		TargetDeviceID:  targetDeviceID,
		TargetSessionID: sessionID,
		EphemeralPubkey: ephPub,
		Candidates:      ourCandidates,
	})
	if err != nil {
		return fmt.Errorf("send connect request: %w", err)
	}

	// Wait for the answer on our dedicated channel.
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()

	var answer remote.ConnectAnswerResponseMsg
	select {
	case answer = <-answerCh:
	case <-timer.C:
		return fmt.Errorf("connect timed out")
	}

	// Establish bridge - P2P if possible, relay fallback
	var bridge remote.Bridge
	if rs.transport != nil {
		cm := remote.NewConnectionManager(rs.transport)
		b, err := cm.Connect(context.Background(), remote.ConnectParams{
			EphemeralPrivKey: ephPriv,
			EphemeralPubKey:  ephPub,
			RemotePubKey:     answer.ResponderPubkey,
			TheirCandidates:  answer.ResponderCandidates,
			RelayAddr:        answer.RelayURL,
			PairingToken:     answer.PairingToken,
			IsInitiator:      true,
			P2PTimeout:       remote.DefaultP2PTimeout,
			SkipRelayTLS:     h.SkipRelayTLS,
		})
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		bridge = b
	} else {
		b, err := remote.NewRelayBridge(
			context.Background(),
			answer.RelayURL,
			answer.PairingToken,
			ephPriv,
			answer.ResponderPubkey,
			true,
			h.SkipRelayTLS,
		)
		if err != nil {
			return fmt.Errorf("connect to relay: %w", err)
		}
		bridge = b
	}

	// Register the bridge so IPC client writes go to remote
	client.SetRemoteBridge(bridge, sessionID)

	if rCtx.BroadcastFn != nil {
		rCtx.BroadcastFn("remote.connected", map[string]any{
			"sessionId": sessionID,
			"method":    bridge.Method(),
		})
	}

	// Bridge relay I/O to the IPC client's stream
	done := make(chan struct{}, 1)

	// Relay -> IPC client (terminal output)
	go func() {
		defer func() {
			select {
			case done <- struct{}{}:
			default:
			}
		}()
		for {
			frame, err := bridge.ReadFrame(rs.daemonCtx)
			if err != nil {
				return
			}
			ipcFrame := ipc.EncodeFrame(ipc.Frame{
				Type:      backend.FrameTerminalData,
				SessionID: sessionID,
				Payload:   frame,
			})
			if err := client.WriteIpcFrame(ipcFrame); err != nil {
				return
			}
		}
	}()

	// Clean up on disconnect. Guard against double-close: cleanupClient
	// in server.go may have already called bridge.Close() if the IPC
	// client disconnected first.
	go func() {
		<-done
		b, _ := client.GetRemoteBridge()
		if b == bridge {
			bridge.Close()
			client.SetRemoteBridge(nil, "")
		}
	}()

	return nil
}

// Devices returns information about all remote devices and their sessions.
func (rs *RemoteSubsystem) Devices() []map[string]any {
	var all []map[string]any
	rs.states.Range(func(_, val any) bool {
		state := val.(*remote.RemoteState)
		for _, snap := range state.Snapshot() {
			sessions := make([]map[string]any, len(snap.Sessions))
			for i, s := range snap.Sessions {
				sessions[i] = map[string]any{
					"id":            s.ID,
					"name":          s.Name,
					"device_id":     s.DeviceID,
					"device_name":   s.DeviceName,
					"created":       s.Created,
					"last_attached": s.LastAttached,
				}
			}
			all = append(all, map[string]any{
				"id":         snap.DeviceID,
				"name":       snap.DeviceName,
				"owner_name": snap.AccountName,
				"online":     snap.Online,
				"last_seen":  snap.LastSeen,
				"team_id":    snap.TeamID,
				"team_name":  snap.TeamName,
				"sessions":   sessions,
			})
		}
		return true
	})
	if all == nil {
		all = []map[string]any{}
	}
	return all
}

// Close disconnects from signaling and shuts down the QUIC transport.
func (rs *RemoteSubsystem) Close() {
	rs.Disconnect()
	if rs.transport != nil {
		_ = rs.transport.Close()
	}
}
