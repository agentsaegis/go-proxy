package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/agentsaegis/go-proxy/internal/client"
	"github.com/agentsaegis/go-proxy/internal/config"
	"github.com/agentsaegis/go-proxy/internal/daemon"
	"github.com/agentsaegis/go-proxy/internal/server"
	"github.com/agentsaegis/go-proxy/internal/trap"
)

var (
	daemonFlag     bool
	debugFlag      bool
	superDebugFlag bool
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the AgentsAegis proxy server",
	RunE:  runStart,
}

func init() {
	startCmd.Flags().BoolVar(&daemonFlag, "daemon", false, "Run as background daemon")
	startCmd.Flags().BoolVar(&debugFlag, "debug", false, "Enable verbose debug logging")
	startCmd.Flags().BoolVar(&superDebugFlag, "super-debug", false, "Single canary trap on first command for safety testing")
	rootCmd.AddCommand(startCmd)
}

func runStart(_ *cobra.Command, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// --super-debug implies --debug
	if superDebugFlag {
		debugFlag = true
	}

	// --debug flag overrides config log level
	if debugFlag {
		cfg.LogLevel = "debug"
	}

	// In daemon mode, fork and exit the parent process
	if daemonFlag {
		return startDaemon(cfg)
	}

	return runServer(cfg)
}

func startDaemon(cfg *config.Config) error {
	if err := config.EnsureConfigDir(); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	configDir, err := config.ConfigDir()
	if err != nil {
		return fmt.Errorf("getting config directory: %w", err)
	}

	// Check if already running
	pid, readErr := daemon.ReadPID(configDir)
	if readErr == nil && daemon.IsRunning(pid) {
		return fmt.Errorf("AgentsAegis proxy is already running (PID %d)", pid)
	}

	logPath := daemon.LogFile(configDir)

	// Re-exec ourselves without --daemon, redirecting output to log file
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable path: %w", err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}

	args := []string{"start"}
	if superDebugFlag {
		args = append(args, "--super-debug")
	} else if debugFlag {
		args = append(args, "--debug")
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("starting daemon: %w", err)
	}

	_ = logFile.Close()

	// Write child PID
	childPID := cmd.Process.Pid
	if err := os.WriteFile(daemon.PIDFile(configDir), []byte(fmt.Sprintf("%d", childPID)), 0o600); err != nil {
		return fmt.Errorf("writing PID file: %w", err)
	}

	fmt.Printf("AgentsAegis proxy started in background (PID %d)\n", childPID)
	fmt.Printf("Logs: %s\n", logPath)
	fmt.Printf("Listening on: http://localhost:%d\n", cfg.ProxyPort)

	return nil
}

func runServer(cfg *config.Config) error {
	// Set up structured logging
	logLevel := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	// Write PID file for the foreground process too (so status/stop work)
	if err := config.EnsureConfigDir(); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	configDir, err := config.ConfigDir()
	if err != nil {
		return fmt.Errorf("getting config directory: %w", err)
	}
	if err := daemon.WritePID(configDir); err != nil {
		logger.Warn("failed to write PID file", "error", err)
	}
	defer func() {
		if removeErr := daemon.RemovePID(configDir); removeErr != nil {
			logger.Warn("failed to remove PID file", "error", removeErr)
		}
	}()

	var templates []*trap.Template
	var orgConfig trap.OrgConfig
	var apiClient *client.Client

	if superDebugFlag {
		// SUPER DEBUG MODE: single canary trap, fires on first command
		canary := trap.CanaryTemplate()
		templates = []*trap.Template{canary}
		orgConfig = trap.OrgConfig{
			TrapFrequency:  1,
			MaxTrapsPerDay: 999,
			Categories:     []string{"debug_canary"},
		}

		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  ======================================")
		fmt.Fprintln(os.Stderr, "  SUPER DEBUG MODE")
		fmt.Fprintln(os.Stderr, "  Canary trap fires on first bash command")
		fmt.Fprintf(os.Stderr, "  Trap command: %s\n", canary.TrapCommands[0])
		fmt.Fprintln(os.Stderr, "  Verify: ls /tmp/.aegis_canary_*")
		fmt.Fprintln(os.Stderr, "  If file exists -> hook FAILED to block")
		fmt.Fprintln(os.Stderr, "  ======================================")
		fmt.Fprintln(os.Stderr, "")

		logger.Info("super debug mode active",
			"canary_command", canary.TrapCommands[0],
			"trap_frequency", 1,
		)

		// Create API client for event reporting in super-debug mode too
		if cfg.APIToken != "" {
			apiClient = client.New(cfg.DashboardURL, cfg.APIToken)
			logger.Info("API client created for event reporting", "url", cfg.DashboardURL)
		}
	} else {
		// Normal mode: load templates from embedded YAML files
		var loadErr error
		templates, loadErr = trap.LoadTemplates()
		if loadErr != nil {
			return fmt.Errorf("loading trap templates: %w", loadErr)
		}
		logger.Info("trap templates loaded", "count", len(templates))

		// Validate all trap commands are inherently harmless
		templates = trap.ValidateTrapSafety(templates, logger)
		if len(templates) == 0 {
			return fmt.Errorf("no safe trap templates remaining after validation")
		}

		// Start with default config
		orgConfig = trap.DefaultOrgConfig()

		// Create dashboard API client and fetch config
		if cfg.APIToken != "" {
			apiClient = client.New(cfg.DashboardURL, cfg.APIToken)

			fetchCtx, cancel := context.WithTimeout(context.Background(), 5*1e9)
			dashConfig, fetchErr := apiClient.FetchConfig(fetchCtx)
			cancel()

			if fetchErr != nil {
				logger.Warn("failed to fetch config from dashboard - using defaults", "error", fetchErr)
			} else {
				logger.Info("connected to AgentsAegis dashboard", "url", cfg.DashboardURL)
				orgConfig = trap.OrgConfig{
					TrapFrequency:  dashConfig.TrapFrequency,
					MaxTrapsPerDay: dashConfig.MaxTrapsPerDay,
					Categories:     dashConfig.TrapCategories,
					Difficulty:     dashConfig.Difficulty,
				}
				logger.Info("dashboard config loaded",
					"trap_frequency", orgConfig.TrapFrequency,
					"max_traps_per_day", orgConfig.MaxTrapsPerDay,
					"categories", orgConfig.Categories,
					"difficulty", orgConfig.Difficulty,
				)
			}
		} else {
			logger.Info("no API token configured - running in offline mode")
		}

		logger.Info("trap engine config",
			"trap_frequency", orgConfig.TrapFrequency,
			"categories", orgConfig.Categories,
		)
	}

	// Create trap engine with config
	engine := trap.NewEngine(orgConfig)

	// Create trap selector with category and difficulty filtering
	selector := trap.NewSelector(templates)
	if len(orgConfig.Categories) > 0 {
		selector.SetAllowedCategories(orgConfig.Categories)
	}
	if orgConfig.Difficulty != "" {
		selector.SetDifficulty(orgConfig.Difficulty)
	}

	// Create and start the server
	srv := server.New(cfg, engine, selector, apiClient, logger)

	if superDebugFlag {
		engine.SetForceInject(true)
		srv.SetSuperDebug()
	}

	// Handle graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start the server in a goroutine
	errCh := make(chan error, 1)
	go func() {
		if serveErr := srv.Start(); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
		close(errCh)
	}()

	// Wait for shutdown signal or server error
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*1e9)
		defer cancel()
		if shutdownErr := srv.Shutdown(shutdownCtx); shutdownErr != nil {
			return fmt.Errorf("graceful shutdown failed: %w", shutdownErr)
		}
		logger.Info("AgentsAegis proxy stopped gracefully")
		return nil
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	}
}
