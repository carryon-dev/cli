package cmd

import (
	"fmt"

	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/spf13/cobra"
)

func newKillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "kill [session]",
		Short: "Kill a session",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(client *ipc.Client) error {
				var sessionID string
				var displayName string
				if len(args) > 0 {
					id, err := resolveSession(client, args[0])
					if err != nil {
						return err
					}
					sessionID = id
					displayName = args[0]
				} else {
					candidate, err := pickOrResolveSession(client)
					if err != nil {
						return err
					}
					sessionID = candidate.ID
					displayName = candidate.Name
					if !promptConfirm(fmt.Sprintf("Kill session %s?", styleAccent(displayName))) {
						return nil
					}
				}
				_, err := client.Call("session.kill", map[string]any{"sessionId": sessionID})
				if err != nil {
					return fmt.Errorf("failed to kill session: %w", err)
				}
				fmt.Printf("Killed: %s\n", styleAccent(displayName))
				return nil
			})
		},
	}
}
