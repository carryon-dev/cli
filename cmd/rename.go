package cmd

import (
	"fmt"

	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/spf13/cobra"
)

func newRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename [session] [name]",
		Short: "Rename a session",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(client *ipc.Client) error {
				var sessionID string
				var currentName string

				if len(args) >= 1 {
					id, err := resolveSession(client, args[0])
					if err != nil {
						return err
					}
					sessionID = id
					currentName = args[0]
				} else {
					candidate, err := pickOrResolveSession(client)
					if err != nil {
						return err
					}
					sessionID = candidate.ID
					currentName = candidate.Name
				}

				var name string
				if len(args) >= 2 {
					name = args[1]
				} else {
					name = promptInput("New name", currentName)
					if name == currentName {
						fmt.Println("Name unchanged.")
						return nil
					}
				}

				_, err := client.Call("session.rename", map[string]any{
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
