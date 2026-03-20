// Package server implements the HTTP proxy server that sits between
// Claude Code and the Anthropic API.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/agentsaegis/go-proxy/internal/client"
	"github.com/agentsaegis/go-proxy/internal/config"
	"github.com/agentsaegis/go-proxy/internal/trap"
)

// Server is the AgentsAegis proxy HTTP server that intercepts traffic between
// Claude Code and the Anthropic API, injecting security awareness traps.
type Server struct {
	httpServer      *http.Server
	proxyHandler    *ProxyHandler
	callbackHandler *trap.CallbackHandler
	hookHandler     *HookHandler
	logger          *slog.Logger
}

// New creates a new proxy server wired with the trap engine, selector,
// API client, and configuration.
func New(
	cfg *config.Config,
	engine *trap.Engine,
	selector *trap.Selector,
	apiClient *client.Client,
	logger *slog.Logger,
	hookSecret ...string,
) *Server {
	callbackHandler := trap.NewCallbackHandler(engine, selector, apiClient, logger, cfg.ProxyPort)

	secret := ""
	if len(hookSecret) > 0 {
		secret = hookSecret[0]
	}
	hookHandler := NewHookHandler(engine, callbackHandler, logger, secret, cfg.ProxyPort)
	if secret == "" {
		logger.Warn("hook endpoint has no secret configured - any local process can call it")
	}

	httpClient := &http.Client{
		// No timeout - SSE streams can run for a long time
		Timeout: 0,
		// Do not follow redirects automatically
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	proxyHandler := NewProxyHandler(
		cfg.AnthropicBaseURL,
		httpClient,
		engine,
		selector,
		callbackHandler,
		apiClient,
		logger,
	)

	s := &Server{
		proxyHandler:    proxyHandler,
		callbackHandler: callbackHandler,
		hookHandler:     hookHandler,
		logger:          logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleProxy)
	mux.HandleFunc("POST /hooks/pre-tool-use", s.handleHook)
	mux.HandleFunc("GET /__aegis/health", s.handleHealth)

	addr := fmt.Sprintf(":%d", cfg.ProxyPort)
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return s
}

// Start begins listening for requests. It blocks until the server is shut down.
func (s *Server) Start() error {
	s.logger.Info("AgentsAegis proxy starting", "addr", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server with the given context deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("AgentsAegis proxy shutting down")
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	s.proxyHandler.HandleProxy(w, r)
}

// SetSuperDebug disables cooldown and jitter on the hook handler for testing.
func (s *Server) SetSuperDebug() {
	s.hookHandler.maxCooldown = 0
	s.hookHandler.disableJitter = true
}

func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	s.hookHandler.HandlePreToolUse(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
		s.logger.Error("failed to write health response", "error", err)
	}
}
