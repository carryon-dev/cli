package cmd

import (
	"fmt"

	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon, local server, and remote status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(client *ipc.Client) error {
				result, err := client.Call("daemon.status", nil)
				if err != nil {
					return fmt.Errorf("failed to get status: %w", err)
				}
				status, ok := result.(map[string]any)
				if !ok {
					return fmt.Errorf("unexpected response from daemon.status")
				}
				fmt.Println(formatUnifiedStatus(status))
				return nil
			})
		},
	}
}
