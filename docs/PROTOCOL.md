# carryOn Protocol Reference

This document describes the two binary protocols used in carryOn:

1. **IPC Protocol** -- between clients (CLI, VS Code extension, web UI) and the daemon.
2. **Holder Protocol** -- between the daemon and individual holder processes (one per native session).

All multi-byte integers are **big-endian** unless stated otherwise.

---

## IPC Protocol

Clients connect to the daemon over a Unix domain socket (or Windows named pipe) and exchange binary frames. The protocol multiplexes JSON-RPC control messages and raw terminal I/O over a single connection.

### Socket Location

| Platform | Path |
|----------|------|
| Unix/macOS | `~/.carryon/daemon.sock` |
| Windows | `\\.\pipe\carryon-{sha256hash}` where `{sha256hash}` is the first 12 hex chars of SHA-256 of the base directory path |

Socket permissions are set to `0600` (owner-only).

### Frame Format

Every message on the wire is a frame with this layout:

```
+--------+-------------------+-----------+---------------------+-----------+
| 1 byte |     4 bytes       |  N bytes  |      4 bytes        |  M bytes  |
+--------+-------------------+-----------+---------------------+-----------+
|  Type  | SessionID Length  | SessionID | Payload Length       |  Payload  |
|        | (uint32, BE)      | (UTF-8)   | (uint32, BE)        |  (bytes)  |
+--------+-------------------+-----------+---------------------+-----------+
```

**Total frame size:** `1 + 4 + N + 4 + M` bytes.

| Field | Size | Description |
|-------|------|-------------|
| Type | 1 byte | Frame type identifier (see below) |
| SessionID Length | 4 bytes | Length of the session ID string, big-endian uint32 |
| SessionID | N bytes | UTF-8 encoded session ID. Empty string (`N=0`) for non-session frames (e.g., JSON-RPC). |
| Payload Length | 4 bytes | Length of the payload, big-endian uint32 |
| Payload | M bytes | Frame-type-specific data |

The minimum parseable header is 5 bytes (1 type + 4 session ID length). After reading the session ID length, the decoder needs `N + 4` more bytes before it can read the payload length, then `M` more bytes for the payload.

### Frame Types

| Type Byte | Name | Direction | Description |
|-----------|------|-----------|-------------|
| `0x01` | FrameTerminalData | Bidirectional | Raw terminal data (PTY output server->client, keyboard input client->server) |
| `0x02` | FrameResize | Client -> Server | Terminal resize request |
| `0x03` | FrameStreamClose | Client -> Server | Detach from a session's stream |
| `0xFF` | FrameJsonRpc | Bidirectional | JSON-RPC 2.0 message (request, response, or notification) |

#### FrameTerminalData (0x01)

**Client -> Server:** The payload is raw bytes to write to the session's PTY stdin. The `SessionID` field identifies which session receives the input. The client must have an active stream (via `session.attach`) for the session.

**Server -> Client:** The payload is raw PTY output bytes from the session. Sent continuously while the client has an active stream.

#### FrameResize (0x02)

**Client -> Server only.** The `SessionID` identifies the target session. The payload is a JSON object:

```json
{"cols": 120, "rows": 40}
```

Both `cols` and `rows` are unsigned 16-bit integers encoded as JSON numbers.

#### FrameStreamClose (0x03)

**Client -> Server only.** Detaches the client's stream from the session identified by `SessionID`. The payload is empty (length 0). After sending this frame, the server stops forwarding terminal data for that session to this client.

#### FrameJsonRpc (0xFF)

The `SessionID` is always empty (length 0). The payload is a UTF-8 JSON-RPC 2.0 message.

**Request (client -> server):**
```json
{
  "jsonrpc": "2.0",
  "method": "session.list",
  "params": {},
  "id": 1
}
```

**Response (server -> client):**
```json
{
  "jsonrpc": "2.0",
  "result": [...],
  "id": 1
}
```

**Error response (server -> client):**
```json
{
  "jsonrpc": "2.0",
  "error": {"code": -32603, "message": "session not found: abc"},
  "id": 1
}
```

**Notification (server -> client, no `id`):**
```json
{
  "jsonrpc": "2.0",
  "method": "session.created",
  "params": {"sessionId": "abc123", "name": "shell", "backend": "native"}
}
```

Standard JSON-RPC error codes used:
- `-32700` -- Parse error (malformed JSON)
- `-32601` -- Method not found
- `-32603` -- Internal error (handler returned an error)

---

### JSON-RPC Methods

#### client.identify

Identifies the connected client to the daemon. Call this immediately after connecting.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `type` | string | No | Client type: `"cli"`, `"vscode"`, `"web"`, `"relay"` |
| `name` | string | No | Display name, e.g. `"VS Code"`, hostname |
| `pid` | number | No | Client process ID (0 or omit for web clients) |

**Response:**
```json
{"ok": true}
```

**Example:**
```json
// Request
{"jsonrpc": "2.0", "method": "client.identify", "params": {"type": "vscode", "name": "VS Code", "pid": 12345}, "id": 1}

// Response
{"jsonrpc": "2.0", "result": {"ok": true}, "id": 1}
```

**Note:** When the CLI is spawned by another process (e.g., the VS Code extension), client type and name can also be set via the `CARRYON_CLIENT_TYPE` and `CARRYON_CLIENT_NAME` environment variables. If set, these values are used as defaults when `client.identify` is called without explicit parameters.

---

#### session.list

Returns all active sessions.

**Parameters:** None (empty object or omit).

**Response:** Array of session objects (see [Session Object Format](#session-object-format)).

**Example:**
```json
// Request
{"jsonrpc": "2.0", "method": "session.list", "params": {}, "id": 2}

// Response
{"jsonrpc": "2.0", "result": [
  {
    "id": "sess-a1b2c3",
    "name": "dev-server",
    "backend": "native",
    "pid": 54321,
    "created": 1711468800000,
    "lastAttached": 1711472400000,
    "cwd": "/home/user/project",
    "command": "/bin/zsh",
    "attachedClients": 1,
    "clients": [
      {"clientId": "client-1", "type": "vscode", "name": "VS Code", "pid": 54321, "connectedAt": 1711497600000},
      {"clientId": "web-1", "type": "web", "name": "Safari iOS", "ip": "192.168.1.5", "pid": 0, "connectedAt": 1711497600000}
    ]
  }
], "id": 2}
```

---

#### session.create

Creates a new terminal session.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | No | Display name for the session. Auto-generated if omitted. |
| `cwd` | string | No | Working directory. Defaults to daemon's cwd. |
| `command` | string | No | Command to run instead of the default shell. |
| `shell` | string | No | Shell executable path. Defaults to user's login shell. |
| `backend` | string | No | Backend to use (`"native"`). Defaults to first available. |
| `device_id` | string | No | Create session on a remote device. When set, the request is forwarded via signaling to the target device's daemon. Omit for local creation. |

**Response:** A session object (see [Session Object Format](#session-object-format)). For remote creates, the response includes `"remote": true` and `"device_id"`.

**Remote create flow:** When `device_id` is provided, the daemon sends a `session.create.request` through the Team DO signaling channel to the target device. The target device creates the session locally and responds with the session ID. If the target device is offline, an error is returned immediately. Timeout: 10 seconds.

**Example:**
```json
// Request
{"jsonrpc": "2.0", "method": "session.create", "params": {"name": "build", "cwd": "/home/user/project"}, "id": 3}

// Response
{"jsonrpc": "2.0", "result": {
  "id": "sess-d4e5f6",
  "name": "build",
  "backend": "native",
  "pid": 67890,
  "created": 1711468800000,
  "cwd": "/home/user/project",
  "command": "/bin/zsh",
  "attachedClients": 0
}, "id": 3}
```

---

#### session.kill

Terminates a session and its underlying process.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `sessionId` | string | Yes | ID of the session to kill |

**Response:**
```json
{"ok": true}
```

---

#### session.rename

Changes the display name of a session.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `sessionId` | string | Yes | ID of the session to rename |
| `name` | string | Yes | New display name |

**Response:**
```json
{"ok": true}
```

---

#### session.resize

Resizes a session's terminal dimensions.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `sessionId` | string | Yes | ID of the session to resize |
| `cols` | number | Yes | New column count (uint16) |
| `rows` | number | Yes | New row count (uint16) |

**Response:**
```json
{"ok": true}
```

---

#### session.scrollback

Returns the current scrollback buffer contents for a session.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `sessionId` | string | Yes | ID of the session |

**Response:** String containing the scrollback buffer content (raw terminal output including escape sequences). Returns `null` or empty string if no scrollback is available.

**Example:**
```json
// Request
{"jsonrpc": "2.0", "method": "session.scrollback", "params": {"sessionId": "sess-a1b2c3"}, "id": 5}

// Response
{"jsonrpc": "2.0", "result": "user@host:~$ ls\nfile1.txt  file2.txt\nuser@host:~$ ", "id": 5}
```

---

#### session.attach

Attaches the client to a session's binary I/O stream. After a successful response, the server begins sending `FrameTerminalData` (0x01) frames for this session, and the client can send `FrameTerminalData` frames to write to the session's PTY.

Works transparently for both local and remote sessions. For local sessions, the server sends scrollback first then starts the live stream. For remote sessions, the daemon establishes an E2E encrypted relay connection to the target device and bridges the relay I/O to the client's stream - the client sees the same frame protocol regardless.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `sessionId` | string | Yes | ID of the session to attach to |

**Response:**
```json
{"streamId": "sess-a1b2c3"}
```

For remote sessions, the response also includes `"remote": true`.

The `streamId` matches the `sessionId` and is used in the `SessionID` field of subsequent binary frames.

**Remote attach flow:** When the session ID is not found locally, the daemon looks it up in RemoteState to find which device owns it, initiates a `connect.request` through signaling, waits for a `connect.answer` with relay URL and pairing token, establishes a RelayBridge (E2E encrypted), and bridges the relay to the client's IPC frames. Timeout: 10 seconds.

**Detaching:** Send a `FrameStreamClose` (0x03) frame with the session ID to detach, or disconnect the client.

---

#### session.subscribe

Subscribes to a session's output via JSON-RPC notifications (instead of binary frames). Useful for clients that need output without a full binary stream attachment (e.g., monitoring/logging).

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `sessionId` | string | Yes | ID of the session to subscribe to |

**Response:**
```json
{"subscriptionId": "output-sess-a1b2c3-1711468800000", "sessionId": "sess-a1b2c3"}
```

After subscribing, the server sends `session.output` notifications (see [Server-Push Notifications](#server-push-notifications)). Cancel with `subscribe.cancel`.

---

#### subscribe.cancel

Cancels an active subscription (output or log follow).

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `subscriptionId` | string | Yes | ID of the subscription to cancel (returned by `session.subscribe` or `daemon.logs` with `follow: true`) |

**Response:**
```json
{"ok": true}
```

---

#### config.get

Reads a configuration value.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `key` | string | Yes | Dot-separated config key, e.g. `"local.port"` |

**Response:** The configuration value (type varies by key -- string, number, boolean, or null if unset).

---

#### config.set

Sets a configuration value.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `key` | string | Yes | Dot-separated config key |
| `value` | string | Yes | New value (always passed as a string) |

**Response:**
```json
{"ok": true}
```

May include a `"warning"` field if the key is unrecognized or the value has caveats:
```json
{"ok": true, "warning": "unknown config key: foo.bar"}
```

**Side effects:** Setting certain keys triggers immediate actions in the daemon:

| Key | Side Effect |
|-----|-------------|
| `local.enabled` | Starts or stops the local HTTP server |
| `local.port` | Restarts the local server if running |
| `local.expose` | Restarts the local server if running |
| `remote.enabled` | Connects or disconnects remote access |
| `logs.level` | Updates the active log level immediately |

Side-effect failures are non-fatal - the config value is still persisted and the response includes a `"warning"` field describing the issue.

---

#### config.reload

Reloads configuration from disk.

**Parameters:** None.

**Response:**
```json
{"ok": true}
```

---

#### config.schema

Returns the full configuration schema with all settings, their metadata, constraints, and current values. GUI clients use this to dynamically build settings UIs.

**Parameters:** None.

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `schemaVersion` | number | Schema format version (currently `1`). Additive changes do not bump this. |
| `groups` | array | Ordered array of setting groups |

**Group object:**

| Field | Type | Description |
|-------|------|-------------|
| `key` | string | Group identifier (e.g. `"default"`, `"local"`) |
| `name` | string | Display name for the group |
| `description` | string | Human-readable group description |
| `settings` | array | Ordered array of setting objects in this group |

**Setting object:**

| Field | Type | Present | Description |
|-------|------|---------|-------------|
| `key` | string | Always | Dot-separated config key (e.g. `"local.port"`) |
| `name` | string | Always | Display label |
| `description` | string | Always | Human-readable description |
| `type` | string | Always | Value type: `"string"`, `"number"`, or `"bool"` |
| `default` | any | Always | Default value |
| `value` | any | Always | Current effective value |
| `enum` | array | When applicable | Allowed string values |
| `min` | number | When applicable | Minimum value (number settings only) |
| `max` | number | When applicable | Maximum value (number settings only) |

**Example:**
```json
// Request
{"jsonrpc": "2.0", "method": "config.schema", "id": 15}

// Response (abbreviated)
{"jsonrpc": "2.0", "result": {
  "schemaVersion": 1,
  "groups": [
    {
      "key": "default",
      "name": "Default Session",
      "description": "Settings for new terminal sessions",
      "settings": [
        {
          "key": "default.backend",
          "name": "Default Backend",
          "description": "Terminal backend to use when creating new sessions",
          "type": "string",
          "default": "native",
          "value": "native",
          "enum": ["native", "tmux"]
        }
      ]
    }
  ]
}, "id": 15}
```

---

#### daemon.status

Returns daemon health and status information.

**Parameters:** None.

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `uptime` | number | Seconds since daemon started (float) |
| `pid` | number | Daemon process ID |
| `backends` | array | Array of `{id: string, available: bool}` |
| `sessions` | number | Count of active sessions |
| `local` | object | Local server status: `{running: bool, port: number, bind: string}` |

**Example:**
```json
{"jsonrpc": "2.0", "result": {
  "uptime": 3600.5,
  "pid": 11111,
  "backends": [{"id": "native", "available": true}],
  "sessions": 3,
  "local": {"running": true, "port": 8384, "bind": "127.0.0.1"}
}, "id": 10}
```

---

#### daemon.logs

Retrieves recent daemon log entries, with optional follow mode.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `last` | number | No | Number of recent entries to return (default: 100) |
| `follow` | bool | No | If `true`, subscribe to new log entries after returning recent ones |
| `level` | string | No | Filter by log level (e.g. `"error"`, `"info"`, `"debug"`) |

**Response (non-follow):**
```json
{"entries": [
  {"time": "2025-01-15T10:30:00Z", "level": "info", "category": "ipc", "message": "Client connected: client-1"}
]}
```

**Response (follow):**
```json
{
  "entries": [...],
  "subscriptionId": "logs-1711468800000-a1b2c3d4",
  "follow": true,
  "level": "error"
}
```

When following, new entries arrive as `daemon.logs.entry` notifications. Cancel with `subscribe.cancel` using the returned `subscriptionId`.

---

#### project.terminals

Returns terminals associated with a project directory.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `path` | string | Yes | Absolute path to the project directory |

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `declared` | array | Terminals declared in project config (currently always `[]`) |
| `associated` | array | Session objects manually associated with this project |

---

#### project.associate

Associates a session with a project directory.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `path` | string | Yes | Absolute path to the project directory |
| `sessionId` | string | Yes | ID of the session to associate |

**Response:**
```json
{"ok": true}
```

---

#### project.disassociate

Removes a session's association with a project directory.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `path` | string | Yes | Absolute path to the project directory |
| `sessionId` | string | Yes | ID of the session to disassociate |

**Response:**
```json
{"ok": true}
```

---

#### `remote.status`

Returns the current remote access connection status.

**Parameters:** None

**Response:**
```json
{
  "connected": false,
  "account_id": "acct-123",
  "device_id": "dev-456",
  "device_name": "desktop-mac"
}
```

Fields `account_id`, `device_id`, and `device_name` are only present when credentials are configured. `connected` indicates whether the daemon is currently connected to the signaling service.

---

#### `remote.devices`

Returns the list of devices on the team. Requires an active signaling connection.

**Parameters:** None

**Response:** Array of device objects. Each device includes its online status, owner info, team info, and the list of sessions currently published by that device.

```json
[
  {
    "id": "device-id",
    "name": "Device Name",
    "owner_name": "User Name",
    "online": true,
    "last_seen": "2026-03-27T10:00:00Z",
    "team_id": "team-id",
    "team_name": "Team Name",
    "sessions": [
      {
        "id": "session-id",
        "name": "session-name",
        "device_id": "device-id",
        "device_name": "Device Name",
        "created": 1711468800,
        "last_attached": 1711472400
      }
    ]
  }
]
```

Returns an empty array if not connected to signaling.

---

### Server-Push Notifications

The server sends JSON-RPC notifications (no `id` field) to all connected clients when session lifecycle events occur. These are delivered as `FrameJsonRpc` (0xFF) frames with an empty session ID.

#### session.created

Sent when a new session is created (by any client).

```json
{
  "jsonrpc": "2.0",
  "method": "session.created",
  "params": {
    "sessionId": "sess-a1b2c3",
    "name": "shell",
    "backend": "native"
  }
}
```

#### session.ended

Sent when a session terminates (process exited or killed).

```json
{
  "jsonrpc": "2.0",
  "method": "session.ended",
  "params": {
    "sessionId": "sess-a1b2c3"
  }
}
```

#### session.renamed

Sent when a session's display name changes.

```json
{
  "jsonrpc": "2.0",
  "method": "session.renamed",
  "params": {
    "sessionId": "sess-a1b2c3",
    "name": "new-name"
  }
}
```

#### session.attached

Sent when a client attaches to a session's stream (via `session.attach`).

```json
{
  "jsonrpc": "2.0",
  "method": "session.attached",
  "params": {
    "sessionId": "sess-a1b2c3",
    "clientId": "client-1"
  }
}
```

#### session.detached

Sent when a client detaches from a session (via `FrameStreamClose`, disconnect, or session ending).

```json
{
  "jsonrpc": "2.0",
  "method": "session.detached",
  "params": {
    "sessionId": "sess-a1b2c3",
    "clientId": "client-1"
  }
}
```

#### session.output

Sent to clients subscribed via `session.subscribe`. Contains base64-encoded terminal output.

```json
{
  "jsonrpc": "2.0",
  "method": "session.output",
  "params": {
    "subscriptionId": "output-sess-a1b2c3-1711468800000",
    "sessionId": "sess-a1b2c3",
    "data": "<base64-encoded bytes>"
  }
}
```

#### daemon.logs.entry

Sent to clients following daemon logs (via `daemon.logs` with `follow: true`).

```json
{
  "jsonrpc": "2.0",
  "method": "daemon.logs.entry",
  "params": {
    "subscriptionId": "logs-1711468800000-a1b2c3d4",
    "entry": {
      "time": "2025-01-15T10:30:00Z",
      "level": "info",
      "category": "ipc",
      "message": "Client connected: client-2"
    }
  }
}
```

#### config.changed

Sent to all connected clients when a configuration value is changed (via `config.set`).

```json
{
  "jsonrpc": "2.0",
  "method": "config.changed",
  "params": {
    "key": "logs.level",
    "value": "debug"
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `key` | string | The config key that changed |
| `value` | any | The new value (typed - string, number, or boolean) |

#### remote.updated

Sent to all connected clients when remote state changes (a device comes online/offline, or a device's session list is updated). Clients should re-fetch via `remote.devices` to get the latest state.

```json
{
  "jsonrpc": "2.0",
  "method": "remote.updated",
  "params": {}
}
```

No payload - this is a broadcast signal only. For more granular updates, clients can also listen to the specific event notifications below.

#### remote.device.online

Sent when a remote device comes online.

```json
{
  "jsonrpc": "2.0",
  "method": "remote.device.online",
  "params": {
    "device_id": "device-uuid",
    "device_name": "MacBook Pro",
    "account_name": "Alice",
    "team_id": "team-uuid"
  }
}
```

#### remote.device.offline

Sent when a remote device goes offline.

```json
{
  "jsonrpc": "2.0",
  "method": "remote.device.offline",
  "params": {
    "device_id": "device-uuid",
    "device_name": "MacBook Pro",
    "team_id": "team-uuid"
  }
}
```

#### remote.sessions.updated

Sent when a remote device's session list changes (sessions created, ended, or renamed on the remote device).

```json
{
  "jsonrpc": "2.0",
  "method": "remote.sessions.updated",
  "params": {
    "device_id": "device-uuid",
    "team_id": "team-uuid"
  }
}
```

---

### Session Object Format

Returned by `session.list` (as array elements) and `session.create`.

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Unique session identifier (e.g., `"sess-a1b2c3"`) |
| `name` | string | Display name |
| `backend` | string | Backend identifier (`"native"`) |
| `pid` | number | PID of the shell process inside the PTY. `0` or absent if not applicable. |
| `created` | number | Unix timestamp in milliseconds when the session was created |
| `lastAttached` | number | Unix timestamp in milliseconds of the last client attachment. `0` or absent if never attached. |
| `cwd` | string | Working directory of the session. May be absent. |
| `command` | string | Command/shell being run. May be absent. |
| `attachedClients` | number | Count of currently attached stream clients |
| `clients` | array | Array of client objects attached to this session (present in `session.list` responses) |

**Client object within `clients` array:**

| Field | Type | Description |
|-------|------|-------------|
| `clientId` | string | Server-assigned client ID (e.g., `"client-1"`) |
| `type` | string | Client type from `client.identify` (`"cli"`, `"vscode"`, `"web"`, `"relay"`, or `""`) |
| `name` | string | Client display name from `client.identify` |
| `pid` | number | Client process ID from `client.identify` (0 for web clients) |
| `ip` | string | Client IP address (present only for web clients) |
| `connectedAt` | number | Unix timestamp in milliseconds when the client connected |

---

### IPC Client Lifecycle

1. **Connect** to the daemon socket.
2. **Identify** by sending `client.identify` with your client type, name, and PID.
3. **Interact** using JSON-RPC methods (`session.list`, `session.create`, etc.).
4. **Attach** to a session with `session.attach` to start binary I/O. Send `FrameTerminalData` frames to write, receive `FrameTerminalData` frames with output.
5. **Detach** by sending a `FrameStreamClose` frame.
6. **Disconnect** by closing the socket. The server automatically cleans up streams and subscriptions.

---

### Web Client Tracking

Web clients connect to the daemon via WebSocket (through the localhost HTTP server). Despite the different transport, web clients are tracked in the same `clients` array that appears in `session.list` responses, alongside IPC clients.

When a web client connects, the daemon automatically captures the following metadata:

| Field | Source | Description |
|-------|--------|-------------|
| `type` | Automatic | Always `"web"` |
| `name` | User-Agent header | Parsed into a human-readable browser/device name (e.g., `"Safari iOS"`, `"Chrome macOS"`) |
| `ip` | Remote address | The client's IP address (e.g., `"192.168.1.5"`, `"127.0.0.1"`) |
| `connectedAt` | Connection time | Unix timestamp in milliseconds when the WebSocket connection was established |
| `pid` | N/A | Always `0` for web clients |

Web clients do not need to send `client.identify` -- the daemon populates their identity from the HTTP upgrade request. If a web client disconnects and reconnects (e.g., due to network interruption or phone wake), it appears as a new entry in the `clients` array with a fresh `connectedAt` timestamp.

---

### Environment Variables

When a parent process spawns `carryon attach`, it can set these environment variables to override the default client identity. The CLI reads them and passes them to `client.identify` on connect.

| Variable | Description | Default |
|----------|-------------|---------|
| `CARRYON_CLIENT_TYPE` | Client type for `client.identify`. | `"cli"` |
| `CARRYON_CLIENT_NAME` | Client display name for `client.identify`. | `"Terminal"` |

Example: an IDE extension spawning a terminal might set `CARRYON_CLIENT_TYPE=vscode` and `CARRYON_CLIENT_NAME=VS Code` so the session's client list shows the correct identity.

---

## Holder Protocol

Each native session runs in a **holder process** -- a lightweight process that owns the PTY and survives daemon restarts. The daemon connects to holders over a local socket and relays I/O between clients and the PTY.

### Socket Location

| Platform | Path |
|----------|------|
| Unix/macOS | `~/.carryon/holders/<session-id>.sock` |
| Windows | `\\.\pipe\carryon-holder-<session-id>` |

Only one connection is active at a time. A new daemon connection replaces any existing one (the old connection is closed).

### Connection Lifecycle

```
Daemon                                          Holder
  |                                                |
  |--- connect to holder socket ------------------>|
  |                                                |
  |<---------- Handshake (binary) -----------------|
  |<---------- Scrollback data (raw bytes) --------|
  |                                                |
  |--- FrameData (keyboard input) --------------->|  \
  |<-- FrameData (PTY output) --------------------|   > Live I/O
  |--- FrameResize ------>------------------------>|  /
  |                                                |
  |--- disconnect (daemon restart, etc.) --------->|
  |                                                |  Holder keeps running,
  |                                                |  PTY stays alive,
  |                                                |  scrollback accumulates.
  |                                                |
  |--- reconnect --------------------------------->|
  |<---------- Handshake + scrollback -------------|  (full state restored)
  |                                                |
  |<---------- FrameExit (shell exited) ----------|  Holder shuts down
```

**Key behaviors:**

- When the daemon connects, the holder sends the handshake followed by the current scrollback buffer. Only then does the holder begin forwarding live PTY output.
- When the daemon disconnects, the holder keeps running. PTY output continues to accumulate in the scrollback buffer (up to 256 KB).
- When the daemon reconnects, it receives a fresh handshake with updated scrollback, restoring full terminal state.
- When the shell process exits, the holder sends a `FrameExit` frame to the connected daemon (if any), closes the socket listener, cleans up the socket file, and terminates.

### Handshake Format

Sent by the holder immediately upon daemon connection, before any frames. This is **not** wrapped in a frame -- it is raw binary data.

```
+----------+------------+--------+--------+--------------+----------+----------+----------+----------+
|  4 bytes |  4 bytes   | 2 bytes| 2 bytes|   4 bytes    | 2 bytes  | N bytes  | 2 bytes  | M bytes  |
+----------+------------+--------+--------+--------------+----------+----------+----------+----------+
|   PID    | HolderPID  |  Cols  |  Rows  | ScrollbackLen|  CwdLen  |   Cwd    |  CmdLen  | Command  |
| (uint32) | (uint32)   |(uint16)|(uint16)|  (uint32)    | (uint16) | (UTF-8)  | (uint16) | (UTF-8)  |
+----------+------------+--------+--------+--------------+----------+----------+----------+----------+
```

**Fixed portion:** 20 bytes (`4 + 4 + 2 + 2 + 4 + 2 + 2`), before variable-length strings.

| Offset | Size | Field | Description |
|--------|------|-------|-------------|
| 0 | 4 | PID | PID of the shell process inside the PTY |
| 4 | 4 | HolderPID | PID of the holder process itself |
| 8 | 2 | Cols | Current terminal column count |
| 10 | 2 | Rows | Current terminal row count |
| 12 | 4 | ScrollbackLen | Number of scrollback bytes that follow the handshake |
| 16 | 2 | CwdLen | Length of the Cwd string |
| 18 | N | Cwd | UTF-8 working directory path |
| 18+N | 2 | CmdLen | Length of the Command string |
| 20+N | M | Command | UTF-8 command/shell string |

**Total handshake size:** `20 + N + M` bytes (where N = CwdLen, M = CmdLen).

Immediately after the handshake bytes, the holder writes `ScrollbackLen` bytes of raw scrollback data. The daemon must read exactly `ScrollbackLen` bytes after parsing the handshake to consume the scrollback dump before entering the frame I/O loop.

### Frame Format

After the handshake + scrollback exchange, all subsequent communication uses this frame format:

```
+--------+--------------+-----------+
| 1 byte |   4 bytes    |  N bytes  |
+--------+--------------+-----------+
|  Type  | Payload Len  |  Payload  |
|        | (uint32, BE) |           |
+--------+--------------+-----------+
```

**Total frame size:** `5 + N` bytes.

This is simpler than the IPC frame format -- there is no session ID field because each holder socket serves exactly one session.

### Frame Types

| Type Byte | Name | Direction | Payload |
|-----------|------|-----------|---------|
| `0x00` | Data | Bidirectional | Raw terminal bytes |
| `0x01` | Resize | Daemon -> Holder | 4 bytes: `[2 bytes cols][2 bytes rows]`, both uint16 big-endian |
| `0x02` | Exit | Holder -> Daemon | 4 bytes: exit code as int32 big-endian |

#### Data (0x00)

**Daemon -> Holder:** Raw keyboard/input bytes to write to the PTY's stdin.

**Holder -> Daemon:** Raw PTY output bytes to relay to attached clients.

#### Resize (0x01)

**Daemon -> Holder only.** Resizes the PTY.

Payload layout:
```
+----------+----------+
|  2 bytes |  2 bytes |
+----------+----------+
|   Cols   |   Rows   |
| (uint16) | (uint16) |
+----------+----------+
```

The holder updates its stored dimensions after resizing the PTY, so subsequent handshakes reflect the new size.

#### Exit (0x02)

**Holder -> Daemon only.** Sent when the shell process exits.

Payload layout:
```
+----------+
|  4 bytes |
+----------+
| ExitCode |
| (int32)  |
+----------+
```

After sending this frame, the holder closes the socket listener, removes the socket file, and terminates. The daemon should treat the session as ended.
