package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/spf13/cobra"
)

func newCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new session",
		RunE: func(cmd *cobra.Command, args []string) error {
			deviceFlag, _ := cmd.Flags().GetString("device")
			nameFlag, _ := cmd.Flags().GetString("name")
			commandFlag, _ := cmd.Flags().GetString("command")
			cwdFlag, _ := cmd.Flags().GetString("cwd")

			return withClient(func(client *ipc.Client) error {
				params := map[string]any{}
				if nameFlag != "" {
					params["name"] = nameFlag
				}
				if commandFlag != "" {
					params["command"] = commandFlag
				}
				if cwdFlag != "" {
					params["cwd"] = cwdFlag
				} else {
					cwd, _ := os.Getwd()
					params["cwd"] = cwd
				}

				if deviceFlag != "" {
					deviceID, err := resolveDevice(client, deviceFlag)
					if err != nil {
						return err
					}
					params["device_id"] = deviceID
				}

				result, err := client.Call("session.create", params)
				if err != nil {
					return fmt.Errorf("failed to create session: %w", err)
				}

				rm, ok := result.(map[string]any)
				if !ok {
					return fmt.Errorf("unexpected response from session.create")
				}
				sessionID, _ := rm["id"].(string)
				sessionName, _ := rm["name"].(string)

				if isRemote, _ := rm["remote"].(bool); isRemote {
					deviceName, _ := rm["device_id"].(string)
					fmt.Printf("Session created on remote device %s.\n", deviceName)
					return nil
				}

				fmt.Printf("Created session: %s %s\n", styleAccent(sessionName), styleID("("+sessionID+")"))

				return interactiveAttach(client, sessionID)
			})
		},
	}

	cmd.Flags().StringP("device", "d", "", "create session on a remote device")
	cmd.Flags().StringP("name", "n", "", "session name")
	cmd.Flags().StringP("command", "c", "", "command to run")
	cmd.Flags().String("cwd", "", "working directory")

	return cmd
}

// matchDevice finds a device from a list of device maps by name or ID.
// Exact match is tried first; if no exact match, prefix match is used.
// Returns the matched device map, or an error if no match or multiple matches.
func matchDevice(input string, devices []map[string]any) (map[string]any, error) {
	// Exact name or ID match first
	for _, dm := range devices {
		name, _ := dm["name"].(string)
		id, _ := dm["id"].(string)
		if name == input || id == input {
			return dm, nil
		}
	}

	// Prefix match on name or ID
	var matches []map[string]any
	for _, dm := range devices {
		name, _ := dm["name"].(string)
		id, _ := dm["id"].(string)
		if strings.HasPrefix(name, input) || strings.HasPrefix(id, input) {
			matches = append(matches, dm)
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("no device matching %q", input)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("multiple devices matching %q - be more specific", input)
	}
	return matches[0], nil
}

// resolveDevice matches a device name or ID from remote.devices.
// Returns the device ID. Fails if the device is offline.
func resolveDevice(client *ipc.Client, input string) (string, error) {
	result, err := client.Call("remote.devices", nil)
	if err != nil {
		return "", fmt.Errorf("failed to list remote devices: %w", err)
	}

	devices, ok := result.([]any)
	if !ok || len(devices) == 0 {
		return "", fmt.Errorf("no remote devices available")
	}

	// Convert to []map[string]any
	deviceMaps := make([]map[string]any, 0, len(devices))
	for _, d := range devices {
		if dm, ok := d.(map[string]any); ok {
			deviceMaps = append(deviceMaps, dm)
		}
	}

	matched, err := matchDevice(input, deviceMaps)
	if err != nil {
		return "", err
	}

	online, _ := matched["online"].(bool)
	if !online {
		name, _ := matched["name"].(string)
		return "", fmt.Errorf("device %q is offline", name)
	}
	id, _ := matched["id"].(string)
	return id, nil
}
