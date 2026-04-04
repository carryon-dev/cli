package cmd

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/carryon-dev/cli/internal/backend"
)

// formatSessionLine formats a single session as a dense one-liner:
// name  clients  time  id
func formatSessionLine(s backend.Session) string {
	name := styleAccent(padEnd(s.Name, 16))
	clients := padEnd(clientsText(s.AttachedClients), 12)
	ts := padEnd(relativeTime(s.Created), 12)
	id := styleID(s.ID)
	return fmt.Sprintf("  %s  %s  %s  %s", name, clients, ts, id)
}

// formatSessionLines formats multiple sessions, one per line.
// Returns the empty state message when no sessions are provided.
func formatSessionLines(sessions []backend.Session) string {
	if len(sessions) == 0 {
		return fmt.Sprintf("%s\n%s", styleDim("No sessions running."), styleHint("Start one with: carryon"))
	}
	lines := make([]string, len(sessions))
	for i, s := range sessions {
		lines[i] = formatSessionLine(s)
	}
	return strings.Join(lines, "\n")
}

// formatUnifiedStatus formats the full unified status output.
func formatUnifiedStatus(status map[string]any) string {
	var sections []string

	// Daemon section
	pid := "?"
	if v, ok := status["pid"]; ok {
		pid = fmt.Sprintf("%v", v)
	}
	uptime := "?"
	if v, ok := status["uptime"]; ok {
		switch n := v.(type) {
		case float64:
			uptime = formatDuration(time.Duration(n) * time.Second)
		case int:
			uptime = formatDuration(time.Duration(n) * time.Second)
		default:
			uptime = fmt.Sprintf("%v", v)
		}
	}
	sessions := "?"
	if v, ok := status["sessions"]; ok {
		sessions = fmt.Sprintf("%v", v)
	}
	sections = append(sections, strings.Join([]string{
		styleAccent("carryOn daemon"),
		fmt.Sprintf("  %s  %s", styleDim("PID:"), pid),
		fmt.Sprintf("  %s  %s", styleDim("Uptime:"), uptime),
		fmt.Sprintf("  %s  %s", styleDim("Sessions:"), sessions),
	}, "\n"))

	// Local server section
	localLines := []string{styleAccent("Local server")}
	if local, ok := status["local"].(map[string]any); ok {
		enabled, _ := local["enabled"].(bool)
		if enabled {
			localLines = append(localLines, fmt.Sprintf("  %s  %s", styleDim("Enabled:"), "yes"))
			expose, _ := local["expose"].(bool)
			if expose {
				localLines = append(localLines, fmt.Sprintf("  %s  %s", styleDim("Expose:"), "yes (all interfaces)"))
			} else {
				localLines = append(localLines, fmt.Sprintf("  %s  %s", styleDim("Expose:"), "no (localhost only)"))
			}
			if url, ok := local["url"].(string); ok {
				localLines = append(localLines, fmt.Sprintf("  %s  %s", styleDim("URL:"), url))
			}
		} else {
			localLines = append(localLines, fmt.Sprintf("  %s  %s", styleDim("Enabled:"), "no"))
		}
	}
	sections = append(sections, strings.Join(localLines, "\n"))

	// Remote access section
	remoteLines := []string{styleAccent("Remote access")}
	if remote, ok := status["remote"].(map[string]any); ok {
		enabled, _ := remote["enabled"].(bool)
		if enabled {
			remoteLines = append(remoteLines, fmt.Sprintf("  %s  %s", styleDim("Enabled:"), "yes"))
			connected, _ := remote["connected"].(bool)
			if connected {
				remoteLines = append(remoteLines, fmt.Sprintf("  %s  %s", styleDim("Connected:"), "yes"))
			} else {
				remoteLines = append(remoteLines, fmt.Sprintf("  %s  %s", styleDim("Connected:"), "no"))
			}
			if name, ok := remote["device_name"].(string); ok && name != "" {
				remoteLines = append(remoteLines, fmt.Sprintf("  %s  %s", styleDim("Device:"), name))
			}
			if relay, ok := remote["relay"].(string); ok && relay != "" {
				remoteLines = append(remoteLines, fmt.Sprintf("  %s  %s", styleDim("Relay:"), relay))
			}
		} else {
			remoteLines = append(remoteLines, fmt.Sprintf("  %s  %s", styleDim("Enabled:"), "no"))
		}
	}
	sections = append(sections, strings.Join(remoteLines, "\n"))

	// Backends section
	backendLines := []string{styleAccent("Backends")}
	if v, ok := status["backends"]; ok {
		if bs, ok := v.([]any); ok {
			for _, b := range bs {
				if bm, ok := b.(map[string]any); ok {
					id := fmt.Sprintf("%v", bm["id"])
					avail, _ := bm["available"].(bool)
					if avail {
						backendLines = append(backendLines, fmt.Sprintf("  %s  %s", styleDim(padEnd(id+":", 10)), "available"))
					} else {
						backendLines = append(backendLines, fmt.Sprintf("  %s  %s", styleDim(padEnd(id+":", 10)), styleDim("unavailable")))
					}
				}
			}
		}
	}
	sections = append(sections, strings.Join(backendLines, "\n"))

	return strings.Join(sections, "\n\n")
}

// formatDuration returns a short human-friendly duration like "2h 15m".
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// formatLogEntry formats a single log entry with colored level.
func formatLogEntry(entry map[string]any) string {
	ts := ""
	if v, ok := entry["timestamp"]; ok {
		switch n := v.(type) {
		case float64:
			t := time.UnixMilli(int64(n))
			ts = styleDim(t.Format("15:04:05.000"))
		case int64:
			t := time.UnixMilli(n)
			ts = styleDim(t.Format("15:04:05.000"))
		default:
			ts = styleDim(fmt.Sprintf("%v", v))
		}
	}

	level := ""
	if v, ok := entry["level"].(string); ok {
		upper := strings.ToUpper(v)
		padded := padEnd(upper, 5)
		switch upper {
		case "ERROR":
			level = styleError(padded)
		case "WARN":
			level = styleWarn(padded)
		case "DEBUG":
			level = styleDim(padded)
		default:
			level = padded
		}
	}

	component := ""
	if v, ok := entry["component"].(string); ok {
		component = styleDim("[" + v + "]")
	}

	message := ""
	if v, ok := entry["message"].(string); ok {
		message = v
	}

	return fmt.Sprintf("%s %s %s %s", ts, level, component, message)
}


func padEnd(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}
