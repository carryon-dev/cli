package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/carryon-dev/cli/internal/holder"
	"github.com/spf13/cobra"
)

func newHolderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "__holder",
		Hidden: true,
		Short:  "Internal: run a session holder process",
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID, _ := cmd.Flags().GetString("session-id")
			shell, _ := cmd.Flags().GetString("shell")
			cwd, _ := cmd.Flags().GetString("cwd")
			command, _ := cmd.Flags().GetString("command")
			colsStr, _ := cmd.Flags().GetString("cols")
			rowsStr, _ := cmd.Flags().GetString("rows")
			baseDir, _ := cmd.Flags().GetString("base-dir")

			cols, _ := strconv.Atoi(colsStr)
			rows, _ := strconv.Atoi(rowsStr)
			if cols == 0 {
				cols = 80
			}
			if rows == 0 {
				rows = 24
			}

			var shellArgs []string
			if command != "" {
				shellArgs = []string{"-c", command}
			}

			h, err := holder.Spawn(holder.SpawnOpts{
				SessionID: sessionID,
				Shell:     shell,
				Args:      shellArgs,
				Command:   command,
				Cwd:       cwd,
				Cols:      uint16(cols),
				Rows:      uint16(rows),
				BaseDir:   baseDir,
				Env:       os.Environ(),
			})
			if err != nil {
				return fmt.Errorf("spawn holder: %w", err)
			}

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

			select {
			case <-h.Done():
				// Shell exited naturally
			case <-sigCh:
				h.Shutdown()
			}
			return nil
		},
	}

	cmd.Flags().String("session-id", "", "Session ID")
	cmd.Flags().String("shell", "", "Shell to run")
	cmd.Flags().String("cwd", "", "Working directory")
	cmd.Flags().String("command", "", "Command to run")
	cmd.Flags().String("cols", "80", "Terminal columns")
	cmd.Flags().String("rows", "24", "Terminal rows")
	cmd.Flags().String("base-dir", "", "Carryon base directory")

	return cmd
}
