package cmd

import (
	"fmt"

	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/spf13/cobra"
)

func newRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <session> <name>",
		Short: "Rename a session",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(client *ipc.Client) error {
				sessionID, err := resolveSession(client, args[0])
				if err != nil {
					return err
				}
				name := args[1]
				_, err = client.Call("session.rename", map[string]any{
					"sessionId": sessionID,
					"name":      name,
				})
				if err != nil {
					return fmt.Errorf("failed to rename session: %w", err)
				}
				fmt.Printf("Renamed to: %s\n", styleAccent(name))
				return nil
			})
		},
	}
}
