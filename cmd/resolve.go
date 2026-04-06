package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/ipc"
	"golang.org/x/term"
)

// resolveCandidate pairs a session with its location (e.g. "local" or a device name).
type resolveCandidate struct {
	backend.Session
	Location string
}

// resolveSession takes user input (name, ID, or ID prefix) and returns
// the matching session ID. Shows an inline picker if ambiguous.
func resolveSession(client *ipc.Client, input string) (string, error) {
	candidates, err := fetchAllSessions(client)
	if err != nil {
		return "", err
	}

	matches := matchSessions(input, candidates)

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session matching %q", input)
	case 1:
		return matches[0].ID, nil
	default:
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return "", fmt.Errorf("multiple sessions named %q (use ID to disambiguate)", input)
		}
		return pickSession(input, matches)
	}
}

// fetchAllSessions fetches local sessions and, if connected to remote,
// remote sessions from all devices.
func fetchAllSessions(client *ipc.Client) ([]resolveCandidate, error) {
	result, err := client.Call("session.list", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	sessions := toSessionList(result)

	candidates := make([]resolveCandidate, 0, len(sessions))
	for _, s := range sessions {
		candidates = append(candidates, resolveCandidate{Session: s, Location: "local"})
	}

	// Merge remote sessions if connected
	statusResult, statusErr := client.Call("remote.status", nil)
	if statusErr == nil {
		if rm, ok := statusResult.(map[string]any); ok {
			connected, _ := rm["connected"].(bool)
			if connected {
				devicesResult, devErr := client.Call("remote.devices", nil)
				if devErr == nil {
					if devices, ok := devicesResult.([]any); ok {
						for _, d := range devices {
							dm, _ := d.(map[string]any)
							deviceName, _ := dm["name"].(string)
							if rawSessions, ok := dm["sessions"].([]any); ok {
								for _, rs := range rawSessions {
									rsm, _ := rs.(map[string]any)
									sess := backend.Session{
										ID:   stringVal(rsm, "id"),
										Name: stringVal(rsm, "name"),
									}
									if created, ok := rsm["created"].(float64); ok {
										sess.Created = int64(created)
									}
									if la, ok := rsm["last_attached"].(float64); ok {
										sess.LastAttached = int64(la)
									}
									candidates = append(candidates, resolveCandidate{
										Session:  sess,
										Location: deviceName,
									})
								}
							}
						}
					}
				}
			}
		}
	}

	return candidates, nil
}

// matchSessions resolves input against candidates using the priority order:
// 1. exact name match, 2. exact ID match, 3. prefix ID match.
func matchSessions(input string, candidates []resolveCandidate) []resolveCandidate {
	// 1. Exact name match
	var nameMatches []resolveCandidate
	for _, c := range candidates {
		if c.Name == input {
			nameMatches = append(nameMatches, c)
		}
	}
	if len(nameMatches) > 0 {
		return nameMatches
	}

	// 2. Exact ID match
	for _, c := range candidates {
		if c.ID == input {
			return []resolveCandidate{c}
		}
	}

	// 3. Prefix ID match
	var prefixMatches []resolveCandidate
	for _, c := range candidates {
		if strings.HasPrefix(c.ID, input) {
			prefixMatches = append(prefixMatches, c)
		}
	}
	return prefixMatches
}

// showLocation returns true if the candidates span multiple locations,
// meaning the location column should be shown in the picker.
func showLocation(candidates []resolveCandidate) bool {
	if len(candidates) <= 1 {
		return false
	}
	first := candidates[0].Location
	for _, c := range candidates[1:] {
		if c.Location != first {
			return true
		}
	}
	return false
}

// pickSession shows an interactive arrow-key picker and returns the chosen session ID.
func pickSession(input string, candidates []resolveCandidate) (string, error) {
	max := 10
	truncated := len(candidates) > max
	shown := candidates
	if truncated {
		shown = candidates[:max]
	}

	withLoc := showLocation(candidates)
	selected := 0

	stdinFd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		return "", fmt.Errorf("failed to enter raw mode: %w", err)
	}
	defer term.Restore(stdinFd, oldState)

	render := func() {
		// Move cursor to start and clear from here down.
		fmt.Fprintf(os.Stderr, "\r\033[J")
		if input == "" {
			fmt.Fprintf(os.Stderr, "Select a session:\r\n\r\n")
		} else {
			fmt.Fprintf(os.Stderr, "Multiple sessions named %s:\r\n\r\n", styleAccent(input))
		}
		for i, c := range shown {
			arrow := "  "
			if i == selected {
				arrow = styleAccent(" >") + " "
			} else {
				arrow = "   "
			}
			name := styleAccent(padEnd(c.Name, 16))
			loc := ""
			if withLoc {
				loc = padEnd(c.Location, 12)
			}
			clients := clientsText(c.AttachedClients)
			ts := relativeTime(c.Created)
			id := styleID(c.ID)
			fmt.Fprintf(os.Stderr, "%s%s  %s%s  %s  %s\r\n", arrow, name, loc, clients, styleDim(ts), id)
		}
		if truncated {
			fmt.Fprintf(os.Stderr, "%s\r\n", styleDim(fmt.Sprintf("  ...and %d more - use ID to be specific", len(candidates)-max)))
		}
		fmt.Fprintf(os.Stderr, "\r\n%s\r\n", styleHint("up/down select - enter confirm - esc cancel"))
	}

	// Calculate total lines rendered so we can erase on exit.
	lineCount := func() int {
		n := 2 + len(shown) + 2 // header+blank + items + blank+hint
		if truncated {
			n++
		}
		return n
	}

	cleanup := func() {
		// Move up and erase all picker lines.
		lines := lineCount()
		for i := 0; i < lines; i++ {
			fmt.Fprintf(os.Stderr, "\033[A\033[2K")
		}
		fmt.Fprintf(os.Stderr, "\r")
	}

	render()

	buf := make([]byte, 3)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			cleanup()
			return "", fmt.Errorf("failed to read input: %w", err)
		}

		if n == 1 {
			switch buf[0] {
			case 13: // Enter
				cleanup()
				return shown[selected].ID, nil
			case 27: // Esc (standalone)
				cleanup()
				return "", fmt.Errorf("selection cancelled")
			case 3: // Ctrl+C
				cleanup()
				return "", fmt.Errorf("selection cancelled")
			}
		}

		if n == 3 && buf[0] == 27 && buf[1] == 91 {
			switch buf[2] {
			case 65: // Up
				if selected > 0 {
					selected--
				}
			case 66: // Down
				if selected < len(shown)-1 {
					selected++
				}
			}
			// Erase and re-render.
			lines := lineCount()
			for i := 0; i < lines; i++ {
				fmt.Fprintf(os.Stderr, "\033[A")
			}
			render()
		}
	}
}
