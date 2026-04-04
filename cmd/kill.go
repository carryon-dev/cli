package cmd

import (
	"fmt"

	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/spf13/cobra"
)

func newKillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "kill <session>",
		Short: "Kill a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(client *ipc.Client) error {
				sessionID, err := resolveSession(client, args[0])
				if err != nil {
					return err
				}
				_, err = client.Call("session.kill", map[string]any{"sessionId": sessionID})
				if err != nil {
					return fmt.Errorf("failed to kill session: %w", err)
				}
				fmt.Printf("Killed: %s\n", styleAccent(args[0]))
				return nil
			})
		},
	}
}
