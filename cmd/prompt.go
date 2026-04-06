package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/carryon-dev/cli/internal/ipc"
	"golang.org/x/term"
)

// pickOrResolveSession fetches all sessions and either auto-selects (one session),
// shows the interactive picker (multiple), or returns an error (zero or non-terminal).
func pickOrResolveSession(client *ipc.Client) (*resolveCandidate, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("session argument required (not running in a terminal)")
	}

	candidates, err := fetchAllSessions(client)
	if err != nil {
		return nil, err
	}

	switch len(candidates) {
	case 0:
		return nil, fmt.Errorf("no sessions - run %s to create one", styleAccent("carryon create"))
	case 1:
		return &candidates[0], nil
	default:
		id, err := pickSession("", candidates)
		if err != nil {
			return nil, err
		}
		for i := range candidates {
			if candidates[i].ID == id {
				return &candidates[i], nil
			}
		}
		return nil, fmt.Errorf("selected session not found")
	}
}

// promptConfirm shows a y/N prompt and returns true only if the user types y or Y.
func promptConfirm(message string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", message)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(strings.ToLower(line)) == "y"
}

// promptInput shows a prompt with a default value in brackets. Returns the default
// if the user presses Enter without typing anything.
func promptInput(label string, defaultValue string) string {
	if defaultValue != "" {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, defaultValue)
	} else {
		fmt.Fprintf(os.Stderr, "%s: ", label)
	}
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue
	}
	return line
}
