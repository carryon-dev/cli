package cmd

import (
	"fmt"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(client *ipc.Client) error {
				result, err := client.Call("session.list", nil)
				if err != nil {
					return fmt.Errorf("failed to list sessions: %w", err)
				}
				sessions := toSessionList(result)

				statusResult, statusErr := client.Call("remote.status", nil)
				remoteConnected := false
				if statusErr == nil {
					if rm, ok := statusResult.(map[string]any); ok {
						remoteConnected, _ = rm["connected"].(bool)
					}
				}

				if remoteConnected {
					// LOCAL section
					fmt.Printf("%s %s\n", styleAccent("LOCAL"), styleDim("(this machine)"))
					if len(sessions) == 0 {
						fmt.Println(styleDim("  (no sessions)"))
					} else {
						fmt.Println(formatSessionLines(sessions))
					}

					// REMOTE section
					devicesResult, devErr := client.Call("remote.devices", nil)
					if devErr == nil {
						if devices, ok := devicesResult.([]any); ok {
							for _, d := range devices {
								dm, _ := d.(map[string]any)
								name, _ := dm["name"].(string)
								online, _ := dm["online"].(bool)

								fmt.Println()
								status := styleDim("(offline)")
								if online {
									status = styleAccent("(online)")
								}
								ownerName := stringVal(dm, "owner_name")
								label := name
								if label == "" {
									if id, ok := dm["id"].(string); ok {
										label = id
									}
								}
								if ownerName != "" {
									label = label + " (" + ownerName + ")"
								}
								fmt.Printf("%s %s %s\n", styleAccent("REMOTE"), styleDim("- "+label), status)

								var devSessions []backend.Session
								if rawSessions, ok := dm["sessions"].([]any); ok {
									for _, rs := range rawSessions {
										rsm, _ := rs.(map[string]any)
										sess := backend.Session{
											ID:   stringVal(rsm, "id"),
											Name: stringVal(rsm, "name"),
										}
										if created, ok := rsm["created"].(float64); ok {
											sess.Created = int64(created)
										}
										if la, ok := rsm["last_attached"].(float64); ok {
											sess.LastAttached = int64(la)
										}
										devSessions = append(devSessions, sess)
									}
								}

								if len(devSessions) == 0 {
									fmt.Println(styleDim("  (no sessions)"))
								} else {
									fmt.Println(formatSessionLines(devSessions))
								}
							}
						}
					}
				} else {
					if len(sessions) == 0 {
						fmt.Println(styleDim("  (no sessions)"))
					} else {
						fmt.Println(formatSessionLines(sessions))
					}
				}

				return nil
			})
		},
	}
	return cmd
}

func stringVal(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func toSessionList(result any) []backend.Session {
	arr, ok := result.([]any)
	if !ok {
		return nil
	}
	sessions := make([]backend.Session, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		s := backend.Session{}
		if v, ok := m["id"].(string); ok {
			s.ID = v
		}
		if v, ok := m["name"].(string); ok {
			s.Name = v
		}
		if v, ok := m["backend"].(string); ok {
			s.Backend = v
		}
		if v, ok := m["attachedClients"].(float64); ok {
			s.AttachedClients = int(v)
		}
		if v, ok := m["created"].(float64); ok {
			s.Created = int64(v)
		}
		sessions = append(sessions, s)
	}
	return sessions
}
