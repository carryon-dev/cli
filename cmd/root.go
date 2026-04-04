package cmd

import (
	"fmt"
	"os"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/spf13/cobra"
)

var rootCmd *cobra.Command

// withClient ensures the daemon is running, connects a client, runs fn, and disconnects.
func withClient(fn func(client *ipc.Client) error) error {
	baseDir := daemon.GetBaseDir()
	if err := daemon.EnsureDaemon(baseDir); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}
	client := ipc.NewClient()
	socketPath := daemon.GetSocketPath(baseDir)
	if err := client.Connect(socketPath); err != nil {
		return fmt.Errorf("failed to connect to daemon: %w", err)
	}
	defer client.Disconnect()
	return fn(client)
}

func Execute(version string) error {
	daemon.SetVersion(version)
	rootCmd = &cobra.Command{
		Use:     "carryon",
		Short:   "Terminal session manager",
		Version: version,
		RunE:    runDefault,
	}

	rootCmd.PersistentFlags().String("backend", "", "backend to use")
	rootCmd.PersistentFlags().String("name", "", "session name")
	rootCmd.PersistentFlags().String("device", "", "target remote device")

	rootCmd.AddCommand(
		newListCmd(),
		newAttachCmd(),
		newKillCmd(),
		newRenameCmd(),
		newStartCmd(),
		newStopCmd(),
		newStatusCmd(),
		newConfigCmd(),
		newLogsCmd(),
		newProjectCmd(),
		newUpdateCmd(),
		newHolderCmd(),
		newRemoteCmd(),
		newCreateCmd(),
	)

	return rootCmd.Execute()
}

func runDefault(cmd *cobra.Command, args []string) error {
	backendFlag, _ := cmd.Flags().GetString("backend")
	nameFlag, _ := cmd.Flags().GetString("name")
	deviceFlag, _ := cmd.Flags().GetString("device")

	return withClient(func(client *ipc.Client) error {
		cwd, _ := os.Getwd()
		params := map[string]any{
			"cwd": cwd,
		}
		if backendFlag != "" {
			params["backend"] = backendFlag
		}
		if nameFlag != "" {
			params["name"] = nameFlag
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
}
