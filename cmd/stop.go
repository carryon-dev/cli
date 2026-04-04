package cmd

import (
	"fmt"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/spf13/cobra"
)

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := daemon.StopDaemon(daemon.GetBaseDir()); err != nil {
				return fmt.Errorf("failed to stop daemon: %w", err)
			}
			fmt.Println("Daemon stopped.")
			return nil
		},
	}
}
