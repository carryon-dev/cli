package daemon

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/carryon-dev/cli/internal/backend"
	"github.com/carryon-dev/cli/internal/config"
	"github.com/carryon-dev/cli/internal/ipc"
	"github.com/carryon-dev/cli/internal/logging"
	"github.com/carryon-dev/cli/internal/remote"
	"github.com/carryon-dev/cli/internal/session"
	"github.com/carryon-dev/cli/internal/state"
	"github.com/carryon-dev/cli/internal/updater"
)

// DaemonOptions configures the daemon startup.
type DaemonOptions struct {
	BaseDir    string // e.g. ~/.carryon
	InProcess  bool   // when true, holders run in-process (for tests). Default false = detached processes.
	Executable string // override binary path for SpawnProcess (for tests). Empty = os.Executable().
}

// StartDaemon wires up all components and starts the IPC server.
// It returns a shutdown function that cleanly tears everything down.
// The caller should block (e.g. on a signal) and then call shutdown.
func StartDaemon(opts DaemonOptions) (shutdown func(), err error) {
	baseDir := opts.BaseDir
	stateDir := filepath.Join(baseDir, "state")
	logsDir := filepath.Join(baseDir, "logs")
	socketPath := GetSocketPath(baseDir)

	// Ensure directories exist.
	if err := EnsureBaseDir(baseDir); err != nil {
		return nil, fmt.Errorf("ensure base dir: %w", err)
	}

	// Config
	cfg := config.NewManager(baseDir)
	logLevel := cfg.GetString("logs.level")
	maxFiles := cfg.GetInt("logs.maxFiles")

	// Logging
	logStore := logging.NewStore(logsDir, maxFiles)
	logger := logging.NewLogger(logStore, logLevel)
	logger.Info("daemon", "Starting carryon daemon")

	// State
	sessionState := state.NewSessionState(stateDir)

	// Backends
	registry := backend.NewRegistry()
	nativeBackend := backend.NewNativeBackend(baseDir, !opts.InProcess)
	if opts.Executable != "" {
		nativeBackend.SetExecutable(opts.Executable)
	}
	registry.Register(nativeBackend)

	tmuxBackend := backend.NewTmuxBackend()
	if tmuxBackend.Available() {
		registry.Register(tmuxBackend)
		logger.Info("daemon", "tmux backend available")
	}

	// Session manager
	defaultBackend := cfg.GetString("default.backend")
	sessionManager := session.NewManager(registry, defaultBackend)

	// Recovery - check which holders are still alive.
	recovery := RecoverSessions(sessionState, logger, baseDir)
	logger.Info("daemon", fmt.Sprintf(
		"Recovery: %d recovered, %d cleaned",
		recovery.Recovered, recovery.Cleaned,
	))

	// Reconnect recovered sessions to their holders.
	if recovery.Recovered > 0 {
		for _, sess := range sessionState.GetAll() {
			if sess.Backend != "native" {
				continue
			}
			if err := nativeBackend.Recover(sess); err != nil {
				logger.Warn("recovery", fmt.Sprintf("Failed to recover session %s: %v", sess.ID, err))
				sessionState.Remove(sess.ID)
			} else {
				logger.Info("recovery", fmt.Sprintf("Recovered session %s (%s)", sess.Name, sess.ID))
			}
		}
	}

	// Persist native sessions on create/end
	sessionManager.OnSessionCreated(func(sess backend.Session) {
		if sess.Backend == "native" {
			sessionState.Save(sess)
		}
		logger.Info("session", fmt.Sprintf("Created: %s (%s)", sess.Name, sess.ID))
	})

	sessionManager.OnSessionEnded(func(id string) {
		sessionState.Remove(id)
		logger.Info("session", fmt.Sprintf("Ended: %s", id))
	})

	sessionManager.OnSessionRenamed(func(sessionID, name string) {
		if sess := sessionManager.Get(sessionID); sess != nil {
			sessionState.Save(*sess)
		}
		logger.Info("session", fmt.Sprintf("Renamed: %s to %s", sessionID, name))
	})

	sessionManager.OnSessionAttached(func(sessionID string) {
		if sess := sessionManager.Get(sessionID); sess != nil {
			sessionState.Save(*sess)
		}
	})

	// IPC server
	rpcCtx := &ipc.RpcContext{
		SessionManager: sessionManager,
		Config:         cfg,
		Logger:         logger,
		LogStore:       logStore,
		Registry:       registry,
		StartTime:      time.Now(),
		BaseDir:        baseDir,
	}

	ipcServer := ipc.NewServer(socketPath, rpcCtx)
	if err := ipcServer.Start(); err != nil {
		logStore.Close()
		return nil, fmt.Errorf("start IPC server: %w", err)
	}
	logger.Info("daemon", fmt.Sprintf("IPC server listening on %s", socketPath))

	// Auto-start local web server if configured.
	ipcServer.AutoStartLocal()

	// Daemon-wide context - cancelled on shutdown to bound goroutine lifetimes.
	daemonCtx, daemonCancel := context.WithCancel(context.Background())

	// Wire up remote subsystem if credentials exist.
	var remoteSub *RemoteSubsystem
	remotePath := filepath.Join(baseDir, "remote")
	remoteCreds, credErr := remote.LoadCredentials(remotePath)
	if credErr == nil && remoteCreds != nil {
		remoteSub = NewRemote(RemoteOpts{
			Creds:          remoteCreds,
			RemotePath:     remotePath,
			Config:         cfg,
			Logger:         logger,
			SessionManager: sessionManager,
			DaemonCtx:      daemonCtx,
			BroadcastFn:    rpcCtx.BroadcastFn,
		})
		rpcCtx.Remote = remoteSub

		// Auto-connect if configured.
		if cfg.GetBool("remote.enabled") && cfg.GetBool("remote.autoconnect") {
			go remoteSub.Connect()
		}
	}

	// PID file
	if err := WritePidFile(baseDir); err != nil {
		logger.Warn("daemon", fmt.Sprintf("Failed to write PID file: %v", err))
	} else {
		logger.Info("daemon", fmt.Sprintf("PID %d written", os.Getpid()))
	}

	// Background update checker - downloads new versions so they can be
	// applied on the next "carryon update" or daemon restart.
	if cfg.GetBool("updates.enabled") && !IsDevMode() {
		checker := updater.NewChecker(baseDir, version)
		go func() {
			// Check on startup if enough time has passed.
			if checker.ShouldCheck() {
				backgroundUpdateCheck(checker, logger)
			}

			ticker := time.NewTicker(12 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if checker.ShouldCheck() {
						backgroundUpdateCheck(checker, logger)
					}
				case <-daemonCtx.Done():
					return
				}
			}
		}()
	}

	// Shutdown handler - safe to call multiple times (signal handler + caller).
	var shutdownOnce sync.Once
	shutdown = func() {
		shutdownOnce.Do(func() {
			logger.Info("daemon", "Shutting down")
			daemonCancel()
			if remoteSub != nil {
				remoteSub.Close()
			}
			_ = ipcServer.StopLocalhost()
			_ = ipcServer.Stop()
			sessionManager.Shutdown()
			sessionState.Flush()
			logStore.Close()
			RemovePidFile(baseDir)
		})
	}

	// Signal handling (SIGTERM, SIGINT -> shutdown)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		shutdown()
		os.Exit(0)
	}()

	return shutdown, nil
}

// backgroundUpdateCheck checks for updates and downloads if available.
// Runs in a goroutine - logs results but never blocks the daemon.
func backgroundUpdateCheck(checker *updater.Checker, logger *logging.Logger) {
	info, err := checker.Check()
	if err != nil {
		logger.Warn("updater", fmt.Sprintf("Background update check failed: %v", err))
		return
	}
	checker.RecordCheck()

	if !info.Available {
		logger.Info("updater", fmt.Sprintf("Up to date (%s)", info.CurrentVersion))
		return
	}

	logger.Info("updater", fmt.Sprintf("Update available: %s -> %s, downloading...", info.CurrentVersion, info.LatestVersion))
	if _, err := checker.Download(info); err != nil {
		logger.Warn("updater", fmt.Sprintf("Background update download failed: %v", err))
		return
	}
	logger.Info("updater", fmt.Sprintf("Update %s downloaded - will apply on next 'carryon update' or restart", info.LatestVersion))
}
