package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "View daemon logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			last, _ := cmd.Flags().GetInt("last")
			level, _ := cmd.Flags().GetString("level")
			follow, _ := cmd.Flags().GetBool("follow")

			return withClient(func(client *ipc.Client) error {
				params := map[string]any{
					"last":   last,
					"follow": follow,
				}
				if level != "" {
					params["level"] = level
				}

				result, err := client.Call("daemon.logs", params)
				if err != nil {
					return fmt.Errorf("failed to get logs: %w", err)
				}

				rm, ok := result.(map[string]any)
				if !ok {
					return fmt.Errorf("unexpected response from daemon.logs")
				}

				// Print existing entries
				if entries, ok := rm["entries"].([]any); ok {
					for _, entry := range entries {
						if em, ok := entry.(map[string]any); ok {
							fmt.Println(formatLogEntry(em))
						}
					}
				}

				// If following, set up notification listener and block
				if follow {
					subscriptionID, _ := rm["subscriptionId"].(string)

					client.OnNotification("daemon.logs.entry", func(params map[string]any) {
						if entry, ok := params["entry"].(map[string]any); ok {
							fmt.Println(formatLogEntry(entry))
						}
					})

					// Block until Ctrl+C
					sigCh := make(chan os.Signal, 1)
					signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
					<-sigCh

					// Cancel subscription
					if subscriptionID != "" {
						client.Call("subscribe.cancel", map[string]any{
							"subscriptionId": subscriptionID,
						})
					}
				}

				return nil
			})
		},
	}

	cmd.Flags().Int("last", 50, "show last N entries")
	cmd.Flags().String("level", "", "filter by level")
	cmd.Flags().BoolP("follow", "f", false, "follow log output")

	return cmd
}
