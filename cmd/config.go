package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newConfigCmd() *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration",
	}

	// config get
	getCmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Get a config value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			return withClient(func(client *ipc.Client) error {
				result, err := client.Call("config.get", map[string]any{"key": key})
				if err != nil {
					return fmt.Errorf("failed to get config: %w", err)
				}
				fmt.Println(result)
				return nil
			})
		},
	}

	// config set
	setCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			if key == "local.password" {
				return setPasswordInteractive()
			}

			if len(args) < 2 {
				return fmt.Errorf("usage: carryon config set <key> <value>")
			}

			value := args[1]
			return withClient(func(client *ipc.Client) error {
				result, err := client.Call("config.set", map[string]any{
					"key":   key,
					"value": value,
				})
				if err != nil {
					return fmt.Errorf("failed to set config: %w", err)
				}
				rm, ok := result.(map[string]any)
				if ok {
					warning, _ := rm["warning"].(string)
					pw, hasPw := rm["generated_password"].(string)
					if warning != "" && hasPw {
						fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
						fmt.Printf("\n  Web access password: %s\n", pw)
						fmt.Println("  Required for all connections while expose is enabled.")
						fmt.Println("  To change it: carryon config set local.password")
						fmt.Println()
					} else if warning != "" {
						fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
					}
				}
				fmt.Println("OK")
				return nil
			})
		},
	}

	// config reload
	reloadCmd := &cobra.Command{
		Use:   "reload",
		Short: "Reload config from disk",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(client *ipc.Client) error {
				_, err := client.Call("config.reload", nil)
				if err != nil {
					return fmt.Errorf("failed to reload config: %w", err)
				}
				fmt.Println("Config reloaded.")
				return nil
			})
		},
	}

	// config path
	pathCmd := &cobra.Command{
		Use:   "path",
		Short: "Show config file location",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(filepath.Join(daemon.GetBaseDir(), "config.json"))
		},
	}

	configCmd.AddCommand(getCmd, setCmd, reloadCmd, pathCmd)
	return configCmd
}

func setPasswordInteractive() error {
	fmt.Print("Enter new password (min 8 characters): ")
	pw1, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return fmt.Errorf("failed to read password: %w", err)
	}

	fmt.Print("Confirm password: ")
	pw2, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return fmt.Errorf("failed to read password: %w", err)
	}

	if string(pw1) != string(pw2) {
		return fmt.Errorf("passwords do not match")
	}

	password := string(pw1)
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}

	return withClient(func(client *ipc.Client) error {
		_, err := client.Call("local.set-password", map[string]any{
			"password": password,
		})
		if err != nil {
			return fmt.Errorf("failed to set password: %w", err)
		}
		fmt.Println("Password updated. All web clients have been disconnected.")
		return nil
	})
}
