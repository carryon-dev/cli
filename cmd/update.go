package cmd

import (
	"fmt"

	"github.com/carryon-dev/cli/internal/daemon"
	"github.com/carryon-dev/cli/internal/updater"
	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for and install updates",
		RunE: func(cmd *cobra.Command, args []string) error {
			check, _ := cmd.Flags().GetBool("check")
			baseDir := daemon.GetBaseDir()
			version := cmd.Root().Version

			applier := updater.NewApplier(baseDir)
			checker := updater.NewChecker(baseDir, version)

			if check {
				return runUpdateCheck(checker)
			}

			return runUpdate(checker, applier)
		},
	}

	cmd.Flags().Bool("check", false, "check and download updates without applying")

	return cmd
}

// runUpdateCheck forces a check for updates (bypasses 24h cache).
func runUpdateCheck(checker *updater.Checker) error {
	fmt.Println("Checking for updates...")

	info, err := checker.Check()
	if err != nil {
		return fmt.Errorf("update check failed: %w", err)
	}

	checker.RecordCheck()

	if !info.Available {
		fmt.Printf("You are on the latest version (%s).\n", info.CurrentVersion)
		return nil
	}

	fmt.Printf("Update available: %s -> %s\n", info.CurrentVersion, info.LatestVersion)
	fmt.Println("Downloading...")

	if _, err := checker.Download(info); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	fmt.Printf("Downloaded to %s\n", info.BinaryPath)
	fmt.Println("Run 'carryon update' to install.")
	return nil
}

// runUpdate checks for a pending update and applies it, or checks for a new one.
func runUpdate(checker *updater.Checker, applier *updater.Applier) error {
	// First, check for a pending downloaded update
	binaryPath, found := applier.HasPendingUpdate()
	if found {
		fmt.Printf("Applying pending update from %s...\n", binaryPath)
		if err := applier.Apply(binaryPath); err != nil {
			return fmt.Errorf("failed to apply update: %w", err)
		}
		fmt.Println("Update applied successfully. Restart carryon to use the new version.")
		return nil
	}

	// No pending update - check for a new one
	fmt.Println("Checking for updates...")

	info, err := checker.Check()
	if err != nil {
		return fmt.Errorf("update check failed: %w", err)
	}

	checker.RecordCheck()

	if !info.Available {
		fmt.Printf("You are on the latest version (%s).\n", info.CurrentVersion)
		return nil
	}

	fmt.Printf("Update available: %s -> %s\n", info.CurrentVersion, info.LatestVersion)
	fmt.Println("Downloading...")

	hash, err := checker.Download(info)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	fmt.Printf("Applying update to %s...\n", info.LatestVersion)
	applier.ExpectedHash = hash
	if err := applier.Apply(info.BinaryPath); err != nil {
		return fmt.Errorf("failed to apply update: %w", err)
	}

	fmt.Println("Update applied successfully. Restart carryon to use the new version.")
	return nil
}
