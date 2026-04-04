package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// UpdateInfo describes an available update.
type UpdateInfo struct {
	CurrentVersion string
	LatestVersion  string
	DownloadURL    string
	ChecksumURL    string // URL for checksums.txt if present
	SignatureURL   string // URL for checksums.txt.sig if present
	BinaryPath     string // path in ~/.carryon/updates/
	Available      bool
}

// Checker checks for updates from GitHub releases.
type Checker struct {
	baseDir        string
	currentVersion string
	repoOwner      string
	repoName       string
}

// updateCheckState stores the time of the last update check.
type updateCheckState struct {
	LastCheck time.Time `json:"lastCheck"`
}

// githubRelease is the relevant portion of the GitHub releases API response.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

// githubAsset represents a release asset from the GitHub API.
type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// NewChecker creates a new Checker.
func NewChecker(baseDir, currentVersion string) *Checker {
	return &Checker{
		baseDir:        baseDir,
		currentVersion: currentVersion,
		repoOwner:      "carryon-dev",
		repoName:       "cli",
	}
}

// Check queries GitHub for the latest release and returns update info.
func (c *Checker) Check() (*UpdateInfo, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", c.repoOwner, c.repoName)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to check for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse release response: %w", err)
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")

	info := &UpdateInfo{
		CurrentVersion: c.currentVersion,
		LatestVersion:  latestVersion,
		Available:      isNewer(latestVersion, c.currentVersion),
	}

	if !info.Available {
		return info, nil
	}

	// Find the binary asset for the current platform
	assetName := buildAssetName(runtime.GOOS, runtime.GOARCH)
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			info.DownloadURL = asset.BrowserDownloadURL
		}
		if asset.Name == "checksums.txt" {
			info.ChecksumURL = asset.BrowserDownloadURL
		}
		if asset.Name == "checksums.txt.sig" {
			info.SignatureURL = asset.BrowserDownloadURL
		}
	}

	if info.DownloadURL == "" {
		return nil, fmt.Errorf("no binary found for %s/%s in release %s", runtime.GOOS, runtime.GOARCH, latestVersion)
	}

	info.BinaryPath = filepath.Join(c.baseDir, "updates", assetName)

	return info, nil
}

// Download downloads the binary to the updates directory and verifies its checksum.
// Returns the verified SHA-256 hex hash so callers can pass it to Applier.ExpectedHash
// for re-verification at apply time (closing the TOCTOU window for same-session updates).
func (c *Checker) Download(info *UpdateInfo) (verifiedHash string, err error) {
	updatesDir := filepath.Join(c.baseDir, "updates")
	if err := os.MkdirAll(updatesDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create updates directory: %w", err)
	}

	// Download the binary
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(info.DownloadURL)
	if err != nil {
		return "", fmt.Errorf("failed to download update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	tmpPath := info.BinaryPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	hasher := sha256.New()
	writer := io.MultiWriter(f, hasher)

	if _, err := io.Copy(writer, resp.Body); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to write update binary: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to finalize download: %w", err)
	}

	// Verify checksum - checksums.txt is required; refuse to install without it
	if info.ChecksumURL == "" {
		os.Remove(tmpPath)
		return "", fmt.Errorf("no checksums.txt found in release - refusing to install unverified binary")
	}
	if info.SignatureURL == "" {
		os.Remove(tmpPath)
		return "", fmt.Errorf("no checksums.txt.sig found in release - refusing to install without signature")
	}
	assetName := filepath.Base(info.BinaryPath)
	checksumData, sigData, err := verifyChecksum(hasher, assetName, info.ChecksumURL, info.SignatureURL, client)
	if err != nil {
		os.Remove(tmpPath)
		return "", err
	}

	hashHex := hex.EncodeToString(hasher.Sum(nil))

	// Save checksums.txt and checksums.txt.sig alongside the binary so
	// ApplyTo can re-verify the ed25519 signature before installing.
	os.WriteFile(filepath.Join(updatesDir, "checksums.txt"), checksumData, 0600)
	os.WriteFile(filepath.Join(updatesDir, "checksums.txt.sig"), sigData, 0600)

	// Move to final path
	if err := os.Rename(tmpPath, info.BinaryPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to finalize update binary: %w", err)
	}

	return hashHex, nil
}

// verifyChecksum downloads checksums.txt, verifies its ed25519 signature,
// then checks the downloaded binary hash against the signed checksums.
// Returns the raw checksums.txt and signature data for local caching.
func verifyChecksum(hasher hash.Hash, assetName, checksumURL, signatureURL string, client *http.Client) (checksumData, sigData []byte, err error) {
	actualHash := hex.EncodeToString(hasher.Sum(nil))

	resp, err := client.Get(checksumURL)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to download checksums: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("checksums download returned status %d", resp.StatusCode)
	}

	checksumData, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read checksums: %w", err)
	}

	// Verify signature before trusting the checksums content.
	sigData, err = verifySignature(checksumData, signatureURL, client)
	if err != nil {
		return nil, nil, fmt.Errorf("checksums.txt signature verification failed: %w", err)
	}

	// Parse checksums.txt - each line is: "<sha256>  <filename>"
	for _, line := range strings.Split(string(checksumData), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == assetName {
			expectedHash := parts[0]
			if actualHash != expectedHash {
				return nil, nil, fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
			}
			return checksumData, sigData, nil
		}
	}

	// No matching entry found - this is a security error, not a skip
	return nil, nil, fmt.Errorf("asset %q not found in checksums.txt", assetName)
}

// ShouldCheck reports whether enough time has passed since the last update check.
// Called by the daemon's background update loop and rate-limited to once per 24h.
func (c *Checker) ShouldCheck() bool {
	statePath := filepath.Join(c.baseDir, "state", "update-check.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return true
	}

	var state updateCheckState
	if err := json.Unmarshal(data, &state); err != nil {
		return true
	}

	return time.Since(state.LastCheck) > 24*time.Hour
}

// RecordCheck writes the current time to the state file.
func (c *Checker) RecordCheck() {
	stateDir := filepath.Join(c.baseDir, "state")
	os.MkdirAll(stateDir, 0700)

	state := updateCheckState{
		LastCheck: time.Now(),
	}
	data, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(stateDir, "update-check.json"), data, 0600)
}

// isNewer returns true if latest is a newer semver than current.
func isNewer(latest, current string) bool {
	latestParts := parseSemver(latest)
	currentParts := parseSemver(current)

	if latestParts == nil && currentParts == nil {
		return false
	}
	if currentParts == nil {
		// Current is "dev" or unparseable - any real version is newer
		return latestParts != nil
	}
	if latestParts == nil {
		return false
	}

	for i := 0; i < 3; i++ {
		if latestParts[i] > currentParts[i] {
			return true
		}
		if latestParts[i] < currentParts[i] {
			return false
		}
	}
	return false
}

// parseSemver parses "1.2.3" into [1, 2, 3]. Returns nil on failure.
func parseSemver(v string) []int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil
	}
	result := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		result[i] = n
	}
	return result
}

// buildAssetName returns the expected release asset name for the given OS and architecture.
func buildAssetName(goos, goarch string) string {
	name := fmt.Sprintf("carryon-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}
