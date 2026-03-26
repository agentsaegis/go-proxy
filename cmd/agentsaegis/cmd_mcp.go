package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/agentsaegis/go-proxy/internal/config"
	"github.com/agentsaegis/go-proxy/internal/mcp"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run as MCP server over stdio for Claude Desktop",
	Long:  "Starts a JSON-RPC 2.0 MCP server reading from stdin and writing to stdout. Used as a child process of Claude Desktop.",
	RunE:  runMCP,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}

func runMCP(_ *cobra.Command, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.DefaultConfig()
	}

	// Log to stderr so stdout is reserved for JSON-RPC
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseSlogLevel(cfg.LogLevel),
	}))

	baseURL := fmt.Sprintf("http://localhost:%d", cfg.ProxyPort)
	hookClient := mcp.NewHookClient(baseURL, "", logger)
	server := mcp.New(hookClient, logger, os.Stdin, os.Stdout)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("MCP server starting", "port", cfg.ProxyPort)
	err = server.Run(ctx)
	if err != nil && err != io.EOF {
		return err
	}
	return nil
}
