package config

import (
	"fmt"
	"strconv"
)

type keyType int

const (
	typeString keyType = iota
	typeNumber
	typeBool
)

type keyDef struct {
	typ      keyType
	defVal   any
	enum     []string
	min, max int // 0 means unconstrained; do not use 0 as an actual boundary value
	name     string
	desc     string
	group    string
	hidden   bool
}

type groupDef struct {
	key  string
	name string
	desc string
}

var Groups = []groupDef{
	{key: "default", name: "Default Session", desc: "Settings for new terminal sessions"},
	{key: "local", name: "Local Server", desc: "Built-in HTTP server for browser access"},
	{key: "remote", name: "Remote Access", desc: "Settings for remote terminal access"},
	{key: "logs", name: "Logging", desc: "Daemon logging configuration"},
	{key: "updates", name: "Updates", desc: "Auto-update behavior"},
}

var KeyOrder = []string{
	"default.backend",
	"default.shell",
	"local.enabled",
	"local.port",
	"local.expose",
	"local.password",
	"remote.enabled",
	"remote.autoconnect",
	"remote.device.name",
	"remote.relay_tls_verify",
	"logs.level",
	"logs.maxFiles",
	"updates.enabled",
}

var Schema = map[string]keyDef{
	"default.backend": {typ: typeString, defVal: "native", enum: []string{"native", "tmux"}, name: "Default Backend", desc: "Terminal backend to use when creating new sessions", group: "default"},
	"default.shell":   {typ: typeString, defVal: "", name: "Default Shell", desc: "Shell to use for new sessions (empty uses system default)", group: "default"},
	"local.enabled": {typ: typeBool, defVal: false, name: "Enabled", desc: "Start the local HTTP server", group: "local"},
	"local.port":    {typ: typeNumber, defVal: 8384, min: 1024, max: 65535, name: "Port", desc: "Port for the local server", group: "local"},
	"local.expose":    {typ: typeBool, defVal: false, name: "Expose", desc: "Expose local server beyond localhost (bind to all interfaces)", group: "local"},
	"local.password":  {typ: typeString, defVal: "", name: "Password", desc: "Bcrypt hash of the web access password (set via local.set-password RPC)", group: "local", hidden: true},
	"remote.enabled":     {typ: typeBool, defVal: false, name: "Enabled", desc: "Connect to signaling on daemon startup", group: "remote"},
	"remote.autoconnect": {typ: typeBool, defVal: true, name: "Auto-Connect", desc: "Auto-connect to signaling when enabled", group: "remote"},
	"remote.device.name":       {typ: typeString, defVal: "", name: "Device Name", desc: "Display name for this device (empty uses hostname)", group: "remote"},
	"remote.relay_tls_verify":  {typ: typeBool, defVal: true, name: "Verify Relay TLS", desc: "Verify TLS certificates when connecting to the relay (disable for self-hosted relays with self-signed certs)", group: "remote"},
	"logs.level":    {typ: typeString, defVal: "info", enum: []string{"debug", "info", "warn", "error"}, name: "Log Level", desc: "Minimum severity level for daemon logs", group: "logs"},
	"logs.maxFiles": {typ: typeNumber, defVal: 5, min: 1, max: 100, name: "Max Log Files", desc: "Number of rotated log files to keep", group: "logs"},
	"updates.enabled": {typ: typeBool, defVal: true, name: "Auto-Update", desc: "Automatically check for and install updates", group: "updates"},
}

func ValidateKey(key, rawValue string) (any, error) {
	def, ok := Schema[key]
	if !ok {
		return nil, fmt.Errorf("unknown config key: %s", key)
	}

	switch def.typ {
	case typeString:
		if len(def.enum) > 0 {
			for _, v := range def.enum {
				if v == rawValue {
					return rawValue, nil
				}
			}
			return nil, fmt.Errorf("must be one of: %v", def.enum)
		}
		return rawValue, nil

	case typeNumber:
		n, err := strconv.Atoi(rawValue)
		if err != nil {
			return nil, fmt.Errorf("must be an integer")
		}
		if def.min != 0 && n < def.min {
			return nil, fmt.Errorf("must be >= %d", def.min)
		}
		if def.max != 0 && n > def.max {
			return nil, fmt.Errorf("must be <= %d", def.max)
		}
		return n, nil

	case typeBool:
		switch rawValue {
		case "true":
			return true, nil
		case "false":
			return false, nil
		default:
			return nil, fmt.Errorf("must be 'true' or 'false'")
		}
	}

	return nil, fmt.Errorf("unknown type for key: %s", key)
}

func GetAllDefaults() map[string]any {
	defaults := make(map[string]any, len(Schema))
	for k, def := range Schema {
		defaults[k] = def.defVal
	}
	return defaults
}

// BuildSchema assembles the full config schema with current values from the manager.
func BuildSchema(mgr *Manager) map[string]any {
	typeNames := map[keyType]string{
		typeString: "string",
		typeNumber: "number",
		typeBool:   "bool",
	}

	// Build settings grouped by group key, preserving KeyOrder
	groupSettings := make(map[string][]map[string]any)
	for _, key := range KeyOrder {
		def := Schema[key]
		if def.hidden {
			continue
		}
		setting := map[string]any{
			"key":         key,
			"name":        def.name,
			"description": def.desc,
			"type":        typeNames[def.typ],
			"default":     def.defVal,
			"value":       mgr.Get(key),
		}
		if len(def.enum) > 0 {
			setting["enum"] = def.enum
		}
		if def.typ == typeNumber && def.min != 0 {
			setting["min"] = def.min
		}
		if def.typ == typeNumber && def.max != 0 {
			setting["max"] = def.max
		}
		groupSettings[def.group] = append(groupSettings[def.group], setting)
	}

	// Assemble groups in defined order
	groups := make([]map[string]any, 0, len(Groups))
	for _, g := range Groups {
		groups = append(groups, map[string]any{
			"key":         g.key,
			"name":        g.name,
			"description": g.desc,
			"settings":    groupSettings[g.key],
		})
	}

	return map[string]any{
		"schemaVersion": 1,
		"groups":        groups,
	}
}
