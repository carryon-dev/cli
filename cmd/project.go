package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/spf13/cobra"
)

func newProjectCmd() *cobra.Command {
	projectCmd := &cobra.Command{
		Use:   "project",
		Short: "Project terminal management",
	}

	// project init
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Create .carryon.json in current directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}
			configPath := filepath.Join(cwd, ".carryon.json")

			if _, err := os.Stat(configPath); err == nil {
				return fmt.Errorf(".carryon.json already exists")
			}

			template := map[string]any{
				"version": 1,
				"terminals": []map[string]string{
					{"name": "example", "command": "echo 'hello from carryon'"},
				},
			}

			data, err := json.MarshalIndent(template, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal template: %w", err)
			}

			if err := os.WriteFile(configPath, append(data, '\n'), 0644); err != nil {
				return fmt.Errorf("failed to write .carryon.json: %w", err)
			}

			fmt.Println("Created .carryon.json")
			return nil
		},
	}

	// project terminals
	terminalsCmd := &cobra.Command{
		Use:   "terminals",
		Short: "List terminals for current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(client *ipc.Client) error {
				cwd, _ := os.Getwd()
				result, err := client.Call("project.terminals", map[string]any{"path": cwd})
				if err != nil {
					return fmt.Errorf("failed to list project terminals: %w", err)
				}
				rm, ok := result.(map[string]any)
				if !ok {
					return fmt.Errorf("unexpected response from project.terminals")
				}
				var matched []any
				if v, ok := rm["matched"].([]any); ok {
					matched = v
				}
				sessions := toSessionList(matched)
				fmt.Println(formatSessionLines(sessions))
				return nil
			})
		},
	}

	projectCmd.AddCommand(initCmd, terminalsCmd)
	return projectCmd
}
