package ipc

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/config"
	"github.com/carryon-dev/cli/internal/holder"
	"github.com/carryon-dev/cli/internal/logging"
	"github.com/carryon-dev/cli/internal/project"
	"github.com/carryon-dev/cli/internal/session"
	"github.com/carryon-dev/cli/internal/state"
	"golang.org/x/crypto/bcrypt"
)

// RemoteService provides remote subsystem operations.
// Nil on RpcContext when no remote credentials are configured.
type RemoteService interface {
	Status() map[string]any
	Connect() error
	Disconnect()
	Devices() []map[string]any
	CreateSession(deviceID string, opts backend.CreateOpts) (string, error)
	AttachSession(client *ClientState, sessionID string, ctx *RpcContext) error
}

// LocalService provides localhost web server operations.
// Nil on RpcContext when localhost server is not wired.
type LocalService interface {
	Start() error
	Stop() error
	Status() map[string]any
	SetPassword(hash string)
	KickWebClients()
}

// RpcContext holds all dependencies needed by RPC handlers.
type RpcContext struct {
	SessionManager *session.Manager
	Config         *config.Manager
	Logger         *logging.Logger
	LogStore       *logging.Store
	Registry       *backend.Registry
	Projects       *state.ProjectAssociations
	StartTime      time.Time
	BaseDir        string

	// Remote subsystem (nil if no remote credentials configured).
	Remote RemoteService

	// Localhost web server (nil until wired by IPC server).
	Local LocalService

	// BroadcastFn sends a JSON-RPC notification to all connected clients.
	BroadcastFn func(method string, params map[string]any)

	// GetSessionClients returns the clients attached to a given session.
	GetSessionClients func(sessionID string) []map[string]any

	// GetWebClients returns web browser clients attached to a given session.
	GetWebClients func(sessionID string) []map[string]any
}

// RpcResult is the value returned by an RPC handler.
// Value is the JSON-serializable result.
// PostRPC, if non-nil, is called after the response is sent.
type RpcResult struct {
	Value   any
	PostRPC func(client *ClientState)
}

// RpcHandler is the signature for an RPC method handler.
type RpcHandler func(params map[string]any, ctx *RpcContext) (RpcResult, error)

// buildMethods returns all registered RPC method handlers.
func buildMethods() map[string]RpcHandler {
	return map[string]RpcHandler{
		"session.list":     sessionList,
		"session.create":   sessionCreate,
		"session.kill":     sessionKill,
		"session.rename":   sessionRename,
		"session.resize":   sessionResize,
		"session.scrollback": sessionScrollback,
		"session.attach":   sessionAttach,
		"config.get":       configGet,
		"config.set":       configSet,
		"config.reload":    configReload,
		"config.schema":    configSchema,
		"daemon.status":    daemonStatus,
		"daemon.logs":      daemonLogs,
		"project.terminals": projectTerminals,
		"project.associate":    projectAssociate,
		"project.disassociate": projectDisassociate,
		"remote.status":        remoteStatus,
		"remote.devices":       remoteDevices,
		"local.set-password":   localSetPassword,
		"subscribe.cancel":     subscribeCancel,
		"client.identify":      clientIdentify,
	}
}

func sessionList(_ map[string]any, ctx *RpcContext) (RpcResult, error) {
	sessions := ctx.SessionManager.List()
	result := make([]map[string]any, 0, len(sessions))
	for _, sess := range sessions {
		entry := map[string]any{
			"id":              sess.ID,
			"name":            sess.Name,
			"backend":         sess.Backend,
			"pid":             sess.PID,
			"created":         sess.Created,
			"lastAttached":    sess.LastAttached,
			"cwd":             sess.Cwd,
			"command":         sess.Command,
			"attachedClients": sess.AttachedClients,
		}
		clients := []map[string]any{}
		if ctx.GetSessionClients != nil {
			if ipcClients := ctx.GetSessionClients(sess.ID); ipcClients != nil {
				clients = append(clients, ipcClients...)
			}
		}
		if ctx.GetWebClients != nil {
			if webClients := ctx.GetWebClients(sess.ID); webClients != nil {
				clients = append(clients, webClients...)
			}
		}
		entry["clients"] = clients
		result = append(result, entry)
	}
	return RpcResult{Value: result}, nil
}

func sessionCreate(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	deviceID, _ := params["device_id"].(string)

	opts := backend.CreateOpts{}
	if v, ok := params["name"].(string); ok {
		opts.Name = v
	}
	if v, ok := params["cwd"].(string); ok {
		opts.Cwd = v
	}
	if v, ok := params["command"].(string); ok {
		opts.Command = v
	}
	if v, ok := params["shell"].(string); ok {
		opts.Shell = v
	}
	if v, ok := params["backend"].(string); ok {
		opts.Backend = v
	}

	// Remote create
	if deviceID != "" {
		if ctx.Remote == nil {
			return RpcResult{}, fmt.Errorf("remote not connected")
		}
		sessionID, err := ctx.Remote.CreateSession(deviceID, opts)
		if err != nil {
			return RpcResult{}, err
		}
		return RpcResult{Value: map[string]any{
			"id":        sessionID,
			"name":      opts.Name,
			"remote":    true,
			"device_id": deviceID,
		}}, nil
	}

	// Local create (existing behavior)
	sess, err := ctx.SessionManager.Create(opts)
	if err != nil {
		return RpcResult{}, err
	}
	return RpcResult{Value: sess}, nil
}

func sessionKill(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	sessionID, ok := params["sessionId"].(string)
	if !ok || sessionID == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: sessionId")
	}
	if err := ctx.SessionManager.Kill(sessionID); err != nil {
		return RpcResult{}, err
	}
	return RpcResult{Value: map[string]any{"ok": true}}, nil
}

func sessionRename(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	sessionID, ok := params["sessionId"].(string)
	if !ok || sessionID == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: sessionId")
	}
	name, ok := params["name"].(string)
	if !ok || name == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: name")
	}
	if err := ctx.SessionManager.Rename(sessionID, name); err != nil {
		return RpcResult{}, err
	}
	return RpcResult{Value: map[string]any{"ok": true}}, nil
}

func sessionResize(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	sessionID, ok := params["sessionId"].(string)
	if !ok || sessionID == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: sessionId")
	}
	cols, ok := toUint16(params["cols"])
	if !ok {
		return RpcResult{}, fmt.Errorf("missing required parameter: cols")
	}
	rows, ok := toUint16(params["rows"])
	if !ok {
		return RpcResult{}, fmt.Errorf("missing required parameter: rows")
	}
	if err := ctx.SessionManager.Resize(sessionID, cols, rows); err != nil {
		return RpcResult{}, err
	}
	return RpcResult{Value: map[string]any{"ok": true}}, nil
}

func sessionScrollback(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	sessionID, ok := params["sessionId"].(string)
	if !ok || sessionID == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: sessionId")
	}
	scrollback := ctx.SessionManager.GetScrollback(sessionID)
	return RpcResult{Value: scrollback}, nil
}

func sessionAttach(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	sessionID, ok := params["sessionId"].(string)
	if !ok || sessionID == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: sessionId")
	}

	// Local session - return holder socket path for direct connection.
	sess := ctx.SessionManager.Get(sessionID)
	if sess != nil {
		holderSocket := holder.SocketPath(ctx.BaseDir, sessionID)
		return RpcResult{
			Value: map[string]any{
				"holderSocket": holderSocket,
				"sessionId":    sessionID,
			},
			PostRPC: func(client *ClientState) {
				// Track the direct attachment so disconnect broadcasts session.detached
				// and getSessionClients includes this client.
				client.mu.Lock()
				client.directSessions[sessionID] = struct{}{}
				client.mu.Unlock()

				if ctx.BroadcastFn != nil {
					ctx.BroadcastFn("session.attached", map[string]any{
						"sessionId": sessionID,
						"clientId":  client.ID,
					})
				}
			},
		}, nil
	}

	// Remote session - keep existing stream-based behavior.
	if ctx.Remote != nil {
		return RpcResult{
			Value: map[string]any{"streamId": sessionID, "remote": true},
			PostRPC: func(client *ClientState) {
				if err := ctx.Remote.AttachSession(client, sessionID, ctx); err != nil {
					ctx.Logger.Warn("remote", fmt.Sprintf("Remote attach failed: %v", err))
				}
			},
		}, nil
	}

	return RpcResult{}, fmt.Errorf("session not found: %s", sessionID)
}

func localSetPassword(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	password, ok := params["password"].(string)
	if !ok || password == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: password")
	}
	if len(password) < 8 {
		return RpcResult{}, fmt.Errorf("password must be at least 8 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return RpcResult{}, fmt.Errorf("failed to hash password: %w", err)
	}

	hashStr := string(hash)
	ctx.Config.Set("local.password", hashStr)

	if ctx.Local != nil {
		ctx.Local.SetPassword(hashStr)
		ctx.Local.KickWebClients()
	}

	return RpcResult{Value: map[string]any{"ok": true}}, nil
}

func configGet(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	key, ok := params["key"].(string)
	if !ok || key == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: key")
	}
	value := ctx.Config.Get(key)
	return RpcResult{Value: value}, nil
}

func configSet(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	key, ok := params["key"].(string)
	if !ok || key == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: key")
	}
	value, ok := params["value"].(string)
	if !ok {
		return RpcResult{}, fmt.Errorf("missing required parameter: value")
	}
	if err := ctx.Config.Set(key, value); err != nil {
		return RpcResult{}, err
	}

	// Capture the config warning before side effects (which may call Config.Set
	// again and clear it - e.g. storing the auto-generated password hash).
	configWarning := ctx.Config.LastWarning()

	// Apply side effects (e.g. start/stop localhost, change log level).
	sideEffectWarning := applyConfigSideEffect(key, ctx)

	if ctx.BroadcastFn != nil {
		ctx.BroadcastFn("config.changed", map[string]any{
			"key":   key,
			"value": ctx.Config.Get(key),
		})
	}
	result := map[string]any{"ok": true}
	if configWarning != "" {
		result["warning"] = configWarning
	}
	if strings.HasPrefix(sideEffectWarning, "generated_password:") {
		result["generated_password"] = strings.TrimPrefix(sideEffectWarning, "generated_password:")
	} else if sideEffectWarning != "" && result["warning"] == nil {
		result["warning"] = sideEffectWarning
	}
	return RpcResult{Value: result}, nil
}

// applyConfigSideEffect triggers runtime actions when certain config keys change.
// Returns a warning string if something went wrong (empty on success).
func applyConfigSideEffect(key string, ctx *RpcContext) string {
	switch key {
	case "local.enabled":
		if ctx.Config.GetBool("local.enabled") {
			if ctx.Local != nil {
				if err := ctx.Local.Start(); err != nil {
					return fmt.Sprintf("config saved but local server failed to start: %v", err)
				}
			}
		} else {
			if ctx.Local != nil {
				if err := ctx.Local.Stop(); err != nil {
					return fmt.Sprintf("config saved but local server failed to stop: %v", err)
				}
			}
		}
	case "local.port":
		if ctx.Config.GetBool("local.enabled") && ctx.Local != nil {
			ctx.Local.Stop()
			if err := ctx.Local.Start(); err != nil {
				return fmt.Sprintf("config saved but local server failed to restart: %v", err)
			}
		}
	case "local.expose":
		// Auto-generate password on first expose
		if ctx.Config.GetBool("local.expose") && ctx.Config.GetString("local.password") == "" {
			password := generatePassword(16)
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err == nil {
				ctx.Config.Set("local.password", string(hash))
				if ctx.Local != nil {
					ctx.Local.SetPassword(string(hash))
				}
				// Return password so the caller can display it once
				return "generated_password:" + password
			}
		}
		if ctx.Config.GetBool("local.enabled") && ctx.Local != nil {
			ctx.Local.Stop()
			if err := ctx.Local.Start(); err != nil {
				return fmt.Sprintf("config saved but local server failed to restart: %v", err)
			}
		}
	case "remote.enabled":
		if ctx.Config.GetBool("remote.enabled") {
			if ctx.Remote != nil {
				if err := ctx.Remote.Connect(); err != nil {
					return fmt.Sprintf("config saved but remote connection failed: %v", err)
				}
			}
		} else {
			if ctx.Remote != nil {
				ctx.Remote.Disconnect()
			}
		}
	case "logs.level":
		if ctx.Logger != nil {
			ctx.Logger.SetLevel(ctx.Config.GetString("logs.level"))
		}
	}
	return ""
}

func configReload(_ map[string]any, ctx *RpcContext) (RpcResult, error) {
	// Snapshot values before reload to detect changes.
	before := make(map[string]any)
	for _, key := range config.KeyOrder {
		before[key] = ctx.Config.Get(key)
	}

	ctx.Config.Reload()

	// Apply side effects for any keys that changed.
	for _, key := range config.KeyOrder {
		after := ctx.Config.Get(key)
		if fmt.Sprintf("%v", before[key]) != fmt.Sprintf("%v", after) {
			applyConfigSideEffect(key, ctx)
		}
	}

	return RpcResult{Value: map[string]any{"ok": true}}, nil
}

func configSchema(_ map[string]any, ctx *RpcContext) (RpcResult, error) {
	return RpcResult{Value: config.BuildSchema(ctx.Config)}, nil
}

func daemonStatus(_ map[string]any, ctx *RpcContext) (RpcResult, error) {
	uptimeSeconds := time.Since(ctx.StartTime).Seconds()
	backends := ctx.Registry.GetAll()
	backendList := make([]map[string]any, 0, len(backends))
	for _, b := range backends {
		backendList = append(backendList, map[string]any{
			"id":        b.ID(),
			"available": b.Available(),
		})
	}
	sessions := ctx.SessionManager.List()
	sessionCount := len(sessions)

	// Local server status
	localStatus := map[string]any{"running": false}
	if ctx.Local != nil {
		localStatus = ctx.Local.Status()
	}
	// Always include config values (even when not running)
	localStatus["enabled"] = ctx.Config.GetBool("local.enabled")
	if _, ok := localStatus["port"]; !ok {
		localStatus["port"] = ctx.Config.GetInt("local.port")
	}
	if _, ok := localStatus["expose"]; !ok {
		localStatus["expose"] = ctx.Config.GetBool("local.expose")
	}

	localStatus["password_set"] = ctx.Config.GetString("local.password") != ""

	// Build local URL for convenience
	host := "127.0.0.1"
	if expose, ok := localStatus["expose"].(bool); ok && expose {
		host = "0.0.0.0"
	}
	scheme := "http"
	if tls, ok := localStatus["tls"].(bool); ok && tls {
		scheme = "https"
	}
	port := localStatus["port"]
	localStatus["url"] = fmt.Sprintf("%s://%s:%v", scheme, host, port)

	// Remote status
	remoteStatus := map[string]any{"enabled": ctx.Config.GetBool("remote.enabled")}
	if ctx.Remote != nil {
		rs := ctx.Remote.Status()
		for k, v := range rs {
			remoteStatus[k] = v
		}
	}

	return RpcResult{Value: map[string]any{
		"uptime":   uptimeSeconds,
		"pid":      os.Getpid(),
		"backends": backendList,
		"sessions": sessionCount,
		"local":    localStatus,
		"remote":   remoteStatus,
	}}, nil
}

func daemonLogs(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	last := 100
	if v, ok := toInt(params["last"]); ok {
		last = v
	}
	follow := false
	if v, ok := params["follow"].(bool); ok {
		follow = v
	}
	level := ""
	if v, ok := params["level"].(string); ok {
		level = v
	}

	if !follow {
		entries := ctx.LogStore.GetRecent(last)
		if level != "" {
			filtered := make([]logging.LogEntry, 0, len(entries))
			for _, e := range entries {
				if e.Level == level {
					filtered = append(filtered, e)
				}
			}
			entries = filtered
		}
		return RpcResult{Value: map[string]any{"entries": entries}}, nil
	}

	subscriptionID := fmt.Sprintf("logs-%d-%s", time.Now().UnixMilli(), randomHex(4))

	// Buffer entries that arrive between subscribe and PostRPC (when the
	// client pointer becomes available). Once PostRPC runs, drain the
	// buffer and switch to direct delivery.
	var bufMu sync.Mutex
	var buf []logging.LogEntry
	var target *ClientState

	entries, unsub := ctx.LogStore.GetRecentAndSubscribe(last, func(entry logging.LogEntry) {
		if level != "" && entry.Level != level {
			return
		}
		msg := map[string]any{
			"jsonrpc": "2.0",
			"method":  "daemon.logs.entry",
			"params": map[string]any{
				"subscriptionId": subscriptionID,
				"entry":          entry,
			},
		}
		bufMu.Lock()
		if target != nil {
			bufMu.Unlock()
			sendRpcToClient(target, msg)
		} else {
			buf = append(buf, entry)
			bufMu.Unlock()
		}
	})

	if level != "" {
		filtered := make([]logging.LogEntry, 0, len(entries))
		for _, e := range entries {
			if e.Level == level {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	return RpcResult{
		Value: map[string]any{
			"entries":        entries,
			"subscriptionId": subscriptionID,
			"follow":         true,
			"level":          level,
		},
		PostRPC: func(client *ClientState) {
			// Drain buffered entries and switch to direct delivery.
			bufMu.Lock()
			target = client
			pending := buf
			buf = nil
			bufMu.Unlock()

			for _, entry := range pending {
				sendRpcToClient(client, map[string]any{
					"jsonrpc": "2.0",
					"method":  "daemon.logs.entry",
					"params": map[string]any{
						"subscriptionId": subscriptionID,
						"entry":          entry,
					},
				})
			}

			client.mu.Lock()
			client.subscriptions[subscriptionID] = unsub
			client.mu.Unlock()
		},
	}, nil
}

func remoteStatus(_ map[string]any, ctx *RpcContext) (RpcResult, error) {
	if ctx.Remote != nil {
		return RpcResult{Value: ctx.Remote.Status()}, nil
	}
	return RpcResult{Value: map[string]any{"connected": false}}, nil
}

func remoteDevices(_ map[string]any, ctx *RpcContext) (RpcResult, error) {
	if ctx.Remote == nil {
		return RpcResult{Value: []map[string]any{}}, nil
	}
	return RpcResult{Value: ctx.Remote.Devices()}, nil
}

func projectTerminals(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	projectPath, ok := params["path"].(string)
	if !ok || projectPath == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: path")
	}

	associations := ctx.Projects.GetForProject(projectPath)
	allSessions := ctx.SessionManager.List()

	// Ad-hoc associated sessions
	associatedSessions := make([]backend.Session, 0)
	for _, a := range associations.Associated {
		for _, s := range allSessions {
			if s.ID == a.SessionID {
				associatedSessions = append(associatedSessions, s)
				break
			}
		}
	}

	// Read project config and match declared terminals against running sessions.
	var declared []project.TerminalMatch
	cfg := project.ReadProjectConfig(projectPath)
	if cfg != nil {
		declared = project.MatchTerminals(cfg.AllTerminals(), allSessions)
	}

	return RpcResult{Value: map[string]any{
		"declared":   declared,
		"associated": associatedSessions,
	}}, nil
}

func projectAssociate(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: path")
	}
	sessionID, ok := params["sessionId"].(string)
	if !ok || sessionID == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: sessionId")
	}
	if err := ctx.Projects.Associate(path, sessionID); err != nil {
		return RpcResult{}, fmt.Errorf("associate: %w", err)
	}
	return RpcResult{Value: map[string]any{"ok": true}}, nil
}

func projectDisassociate(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: path")
	}
	sessionID, ok := params["sessionId"].(string)
	if !ok || sessionID == "" {
		return RpcResult{}, fmt.Errorf("missing required parameter: sessionId")
	}
	if err := ctx.Projects.Disassociate(path, sessionID); err != nil {
		return RpcResult{}, fmt.Errorf("disassociate: %w", err)
	}
	return RpcResult{Value: map[string]any{"ok": true}}, nil
}

func subscribeCancel(params map[string]any, _ *RpcContext) (RpcResult, error) {
	// The actual cancellation is handled by the server in handleRpc.
	_ = params
	return RpcResult{Value: map[string]any{"ok": true}}, nil
}

// --- helpers ---

func toUint16(v any) (uint16, bool) {
	var n float64
	switch val := v.(type) {
	case float64:
		n = val
	case int:
		n = float64(val)
	case int64:
		n = float64(val)
	default:
		return 0, false
	}
	if n < 1 || n > 65535 {
		return 0, false
	}
	return uint16(n), true
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	const hex = "0123456789abcdef"
	out := make([]byte, n*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	return string(out)
}

func generatePassword(length int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	// Use rejection sampling to avoid modulo bias.
	maxUnbiased := byte(256 - 256%len(chars))
	result := make([]byte, length)
	buf := make([]byte, 1)
	for i := 0; i < length; i++ {
		for {
			if _, err := rand.Read(buf); err != nil {
				panic(fmt.Sprintf("crypto/rand failed: %v", err))
			}
			if buf[0] < maxUnbiased {
				break
			}
		}
		result[i] = chars[buf[0]%byte(len(chars))]
	}
	return string(result)
}

func clientIdentify(params map[string]any, ctx *RpcContext) (RpcResult, error) {
	return RpcResult{
		Value: map[string]any{"ok": true},
		PostRPC: func(client *ClientState) {
			client.mu.Lock()
			if v, ok := params["type"].(string); ok {
				client.Info.Type = v
			}
			if v, ok := params["name"].(string); ok {
				client.Info.Name = v
			}
			if v, ok := params["pid"].(float64); ok {
				client.Info.PID = int(v)
			}
			client.mu.Unlock()
		},
	}, nil
}

// setupLogSubscription creates a log follow subscription for a client.
func setupLogSubscription(client *ClientState, subscriptionID, level string, ctx *RpcContext) {
	unsub := ctx.LogStore.Subscribe(func(entry logging.LogEntry) {
		if level != "" && entry.Level != level {
			return
		}
		sendRpcToClient(client, map[string]any{
			"jsonrpc": "2.0",
			"method":  "daemon.logs.entry",
			"params": map[string]any{
				"subscriptionId": subscriptionID,
				"entry":          entry,
			},
		})
	})

	client.mu.Lock()
	client.subscriptions[subscriptionID] = unsub
	client.mu.Unlock()
}

// sendRpcToClient encodes a JSON-RPC message and writes it as a frame.
func sendRpcToClient(client *ClientState, message map[string]any) {
	payload, err := json.Marshal(message)
	if err != nil {
		return
	}
	frame := EncodeFrame(Frame{
		Type:      backend.FrameJsonRpc,
		SessionID: "",
		Payload:   payload,
	})
	client.mu.Lock()
	conn := client.conn
	client.mu.Unlock()
	if conn == nil {
		return
	}
	_, _ = conn.Write(frame)
}
