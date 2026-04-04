package cmd

import (
	"fmt"
	"os"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/spf13/cobra"
)

func newStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			foreground, _ := cmd.Flags().GetBool("foreground")
			baseDir := daemon.GetBaseDir()

			if err := daemon.EnsureBaseDir(baseDir); err != nil {
				return fmt.Errorf("failed to create base dir: %w", err)
			}

			if foreground || os.Getenv("CARRYON_DAEMON") == "1" {
				shutdown, err := daemon.StartDaemon(daemon.DaemonOptions{BaseDir: baseDir})
				if err != nil {
					return fmt.Errorf("failed to start daemon: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Daemon running (PID %d)\n", os.Getpid())
				_ = shutdown
				select {}
			}

			if err := daemon.EnsureDaemon(baseDir); err != nil {
				return fmt.Errorf("failed to start daemon: %w", err)
			}
			fmt.Println("Daemon started.")
			return nil
		},
	}
	cmd.Flags().Bool("foreground", false, "run in foreground")
	return cmd
}
