package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/carryon-dev/cli/internal/crypto"
	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/carryon-dev/cli/internal/remote"
	"github.com/spf13/cobra"
)

func newRemoteCmd() *cobra.Command {
	remoteCmd := &cobra.Command{
		Use:   "remote",
		Short: "Remote access management",
	}

	remoteCmd.AddCommand(
		newRemoteLoginCmd(),
		newRemoteLogoutCmd(),
		newRemoteStatusCmd(),
		newRemoteDevicesCmd(),
	)

	return remoteCmd
}

func remoteDir() string {
	return filepath.Join(daemon.GetBaseDir(), "remote")
}

func newRemoteLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate this device for remote access",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := remoteDir()
			apiBase := daemon.APIBaseURL()

			// Step 1: Generate device keypair
			pub, priv, err := crypto.GenerateKeypair()
			if err != nil {
				return fmt.Errorf("failed to generate keypair: %w", err)
			}

			// Step 2: Request device code
			codeResp, err := http.Post(apiBase+"/api/auth/device/code", "application/json", nil)
			if err != nil {
				return fmt.Errorf("failed to contact signaling server: %w", err)
			}
			defer codeResp.Body.Close()

			if codeResp.StatusCode != 200 {
				body, _ := io.ReadAll(io.LimitReader(codeResp.Body, 512))
				return fmt.Errorf("device code request failed (%d): %s", codeResp.StatusCode, body)
			}

			var codeResult struct {
				DeviceCode      string `json:"device_code"`
				UserCode        string `json:"user_code"`
				VerificationURI string `json:"verification_uri"`
				ExpiresIn       int    `json:"expires_in"`
				Interval        int    `json:"interval"`
			}
			if err := json.NewDecoder(codeResp.Body).Decode(&codeResult); err != nil {
				return fmt.Errorf("failed to parse device code response: %w", err)
			}

			// Step 3: Display URL and code, try to open browser
			activateURL := fmt.Sprintf("%s?code=%s", codeResult.VerificationURI, codeResult.UserCode)

			fmt.Println()
			fmt.Println("  To authorize this device, open:")
			fmt.Println()
			fmt.Printf("    %s\n", activateURL)
			fmt.Println()
			fmt.Printf("  Or enter code manually: %s\n", codeResult.UserCode)
			fmt.Println()

			openBrowser(activateURL)

			fmt.Println("  Waiting for authorization...")

			// Step 4: Poll for token
			interval := time.Duration(codeResult.Interval) * time.Second
			if interval < 3*time.Second {
				interval = 5 * time.Second
			}
			deadline := time.Now().Add(time.Duration(codeResult.ExpiresIn) * time.Second)

			var sessionToken string
			var accountID string
			var teamID string

			for time.Now().Before(deadline) {
				time.Sleep(interval)

				pollBody, _ := json.Marshal(map[string]string{
					"device_code": codeResult.DeviceCode,
				})
				pollResp, err := http.Post(apiBase+"/api/auth/device/token", "application/json", bytes.NewReader(pollBody))
				if err != nil {
					continue // network error, retry
				}

				var pollResult struct {
					SessionToken string `json:"session_token"`
					AccountID    string `json:"account_id"`
					TeamID       string `json:"team_id"`
					Error        string `json:"error"`
				}
				if err := json.NewDecoder(pollResp.Body).Decode(&pollResult); err != nil {
					pollResp.Body.Close()
					continue
				}
				pollResp.Body.Close()

				if pollResult.Error == "authorization_pending" {
					continue
				}
				if pollResult.Error == "expired_token" {
					return fmt.Errorf("device code expired - please try again")
				}
				if pollResult.Error != "" {
					return fmt.Errorf("authorization failed: %s", pollResult.Error)
				}

				if pollResult.SessionToken != "" {
					sessionToken = pollResult.SessionToken
					accountID = pollResult.AccountID
					teamID = pollResult.TeamID
					break
				}
			}

			if sessionToken == "" {
				return fmt.Errorf("authorization timed out - please try again")
			}

			// Step 5: Register device with public key
			hostname, _ := os.Hostname()
			if hostname == "" {
				hostname = "unknown-device"
			}

			registerBody, _ := json.Marshal(map[string]string{
				"session_token": sessionToken,
				"device_name":   hostname,
				"public_key":    base64.StdEncoding.EncodeToString(pub),
				"team_id":       teamID,
			})

			registerResp, err := http.Post(apiBase+"/api/auth/device/register", "application/json", bytes.NewReader(registerBody))
			if err != nil {
				return fmt.Errorf("failed to register device: %w", err)
			}
			defer registerResp.Body.Close()

			if registerResp.StatusCode != 200 {
				body, _ := io.ReadAll(registerResp.Body)
				return fmt.Errorf("device registration failed (%d): %s", registerResp.StatusCode, body)
			}

			var registerResult struct {
				DeviceID  string `json:"device_id"`
				AccountID string `json:"account_id"`
				TeamID    string `json:"team_id"`
				TeamName  string `json:"team_name"`
			}
			if err := json.NewDecoder(registerResp.Body).Decode(&registerResult); err != nil {
				return fmt.Errorf("failed to parse registration response: %w", err)
			}

			// Step 6: Save credentials and keypair
			if err := crypto.SaveKeypair(dir, pub, priv); err != nil {
				return fmt.Errorf("failed to save keypair: %w", err)
			}

			creds := &remote.Credentials{
				AccountID:    accountID,
				DeviceID:     registerResult.DeviceID,
				SessionToken: sessionToken,
				DeviceName:   hostname,
				TeamID:       registerResult.TeamID,
				TeamName:     registerResult.TeamName,
			}
			if err := remote.SaveCredentials(dir, creds); err != nil {
				return fmt.Errorf("failed to save credentials: %w", err)
			}

			fmt.Printf("\n  Device authorized! Registered as %q in team %s.\n\n", hostname, registerResult.TeamID)

			// Notify running daemon to reload config and pick up new credentials.
			notifyDaemonReload()

			return nil
		},
	}
}

func newRemoteLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove remote access credentials from this device",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := remoteDir()
			apiBase := daemon.APIBaseURL()

			// Try to revoke the session on the server
			creds, err := remote.LoadCredentials(dir)
			if err == nil && creds.SessionToken != "" {
				req, _ := http.NewRequest("POST", apiBase+"/api/auth/logout", nil)
				req.Header.Set("Authorization", "Bearer "+creds.SessionToken)
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					resp.Body.Close()
				}
				// Continue even if server revocation fails - still delete local credentials
			}

			if err := remote.DeleteCredentials(dir); err != nil {
				return fmt.Errorf("failed to delete credentials: %w", err)
			}
			fmt.Println("Remote credentials removed.")

			// Notify running daemon to reload config and pick up credential changes.
			notifyDaemonReload()

			return nil
		},
	}
}

func newRemoteStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show remote connection status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(client *ipc.Client) error {
				result, err := client.Call("remote.status", nil)
				if err != nil {
					return fmt.Errorf("failed to get remote status: %w", err)
				}
				rm, ok := result.(map[string]any)
				if !ok {
					fmt.Printf("%s  %s\n", styleDim("Remote:"), styleDim("not connected"))
					return nil
				}
				connected, _ := rm["connected"].(bool)
				if connected {
					deviceID, _ := rm["device_id"].(string)
					fmt.Printf("%s  %s %s\n", styleDim("Remote:"), styleAccent("connected"), styleDim("(device: "+deviceID+")"))
				} else {
					fmt.Printf("%s  %s\n", styleDim("Remote:"), styleDim("not connected"))
				}
				return nil
			})
		},
	}
}

func newRemoteDevicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "List remote devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(client *ipc.Client) error {
				result, err := client.Call("remote.devices", nil)
				if err != nil {
					return fmt.Errorf("failed to list remote devices: %w", err)
				}
				devices, ok := result.([]any)
				if !ok || len(devices) == 0 {
					fmt.Println(styleDim("No remote devices found."))
					return nil
				}
				for _, d := range devices {
					dm, ok := d.(map[string]any)
					if !ok {
						continue
					}
					id, _ := dm["id"].(string)
					name, _ := dm["name"].(string)
					display := name
					if display == "" {
						display = id
					}
					fmt.Printf("  %s  %s\n", styleAccent(padEnd(display, 20)), styleID(id))
				}
				return nil
			})
		},
	}
}

// notifyDaemonReload tries to connect to the running daemon and tell it to
// reload its configuration so it picks up credential changes. If the daemon
// is not running, it prints a hint for the user.
func notifyDaemonReload() {
	baseDir := daemon.GetBaseDir()
	socketPath := daemon.GetSocketPath(baseDir)
	client := ipc.NewClient()
	if err := client.Connect(socketPath); err != nil {
		fmt.Println("Restart the daemon with `carryon stop && carryon start` to apply changes.")
		return
	}
	defer client.Disconnect()
	_, err := client.Call("config.reload", nil)
	if err != nil {
		fmt.Println("Restart the daemon with `carryon stop && carryon start` to apply changes.")
	}
}

// openBrowser tries to open a URL in the default browser.
// Fails silently - the user can always copy the URL manually.
// Set CARRYON_NO_BROWSER=1 to suppress (useful for tests and CI).
func openBrowser(rawURL string) {
	if os.Getenv("CARRYON_NO_BROWSER") == "1" {
		return
	}
	// Validate URL scheme to prevent argument injection via crafted URLs.
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "linux":
		cmd = exec.Command("xdg-open", rawURL)
	case "windows":
		// Empty string title arg prevents argument injection via shell metacharacters.
		cmd = exec.Command("cmd", "/c", "start", "", rawURL)
	default:
		return
	}
	if err := cmd.Start(); err == nil {
		go cmd.Wait() // reap child process to avoid zombies
	}
}
