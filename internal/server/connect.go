package server

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/agentsaegis/go-proxy/internal/client"
	"github.com/agentsaegis/go-proxy/internal/trap"
)

// mitmHosts is the set of hostnames for which TLS MITM interception is performed.
// CONNECT requests to all other hosts are tunnelled as plain TCP without TLS termination.
var mitmHosts = map[string]bool{
	"api.github.com": true,
}

// ConnectHandler handles HTTP CONNECT requests. For known AI API hosts it
// performs TLS MITM to inspect and optionally modify streamed responses.
// For all other hosts it proxies raw TCP bidirectionally.
type ConnectHandler struct {
	caManager          *CAManager
	trapEngine         *trap.Engine
	trapSelector       *trap.Selector
	callbackHandler    *trap.CallbackHandler
	apiClient          *client.Client
	logger             *slog.Logger
	upstreamHTTPClient *http.Client
}

// NewConnectHandler creates a ConnectHandler with the given dependencies.
func NewConnectHandler(
	caManager *CAManager,
	engine *trap.Engine,
	selector *trap.Selector,
	callbackHandler *trap.CallbackHandler,
	apiClient *client.Client,
	logger *slog.Logger,
) *ConnectHandler {
	return &ConnectHandler{
		caManager:       caManager,
		trapEngine:      engine,
		trapSelector:    selector,
		callbackHandler: callbackHandler,
		apiClient:       apiClient,
		logger:          logger,
		upstreamHTTPClient: &http.Client{
			Timeout: 0, // no overall timeout: SSE streams can run indefinitely
			Transport: &http.Transport{
				ResponseHeaderTimeout: 120 * time.Second,
				TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			},
		},
	}
}

// HandleConnect processes an HTTP CONNECT request. It hijacks the connection,
// replies with "200 Connection Established", then either performs TLS MITM
// (for AI API hosts) or plain TCP tunnel (for everything else).
func (ch *ConnectHandler) HandleConnect(w http.ResponseWriter, r *http.Request) {
	targetAddr := r.Host
	if targetAddr == "" {
		targetAddr = r.URL.Host
	}

	host, _, err := net.SplitHostPort(targetAddr)
	if err != nil {
		// No port present - use as-is
		host = targetAddr
	}

	ch.logger.Debug("CONNECT request", "target", targetAddr, "host", host, "mitm", mitmHosts[strings.ToLower(host)])

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		ch.logger.Error("response writer does not support hijacking")
		http.Error(w, "CONNECT not supported", http.StatusInternalServerError)
		return
	}

	conn, _, err := hijacker.Hijack()
	if err != nil {
		ch.logger.Error("hijack failed", "error", err)
		return
	}

	// Acknowledge the tunnel
	if _, writeErr := fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); writeErr != nil {
		ch.logger.Error("failed to write 200 response", "error", writeErr)
		conn.Close()
		return
	}

	if mitmHosts[strings.ToLower(host)] {
		go ch.handleMITMTunnel(conn, host)
	} else {
		go ch.handlePlainTunnel(conn, targetAddr)
	}
}

// handleMITMTunnel wraps the hijacked connection in TLS, then reads HTTP
// requests in a loop and forwards them to the real upstream with trap injection.
func (ch *ConnectHandler) handleMITMTunnel(conn net.Conn, hostname string) {
	defer conn.Close()

	tlsCfg, err := ch.caManager.GetTLSConfig(hostname)
	if err != nil {
		ch.logger.Error("failed to get TLS config for MITM", "hostname", hostname, "error", err)
		return
	}

	tlsConn := tls.Server(conn, tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		ch.logger.Debug("TLS handshake failed", "hostname", hostname, "error", err)
		return
	}
	defer tlsConn.Close()

	reader := bufio.NewReader(tlsConn)
	for {
		req, readErr := http.ReadRequest(reader)
		if readErr != nil {
			if readErr != io.EOF {
				ch.logger.Debug("reading tunneled request", "hostname", hostname, "error", readErr)
			}
			return
		}

		if fwdErr := ch.forwardRequest(tlsConn, req, hostname); fwdErr != nil {
			ch.logger.Debug("forwarding tunneled request failed", "hostname", hostname, "error", fwdErr)
			return
		}
	}
}

// forwardRequest forwards a single HTTP request from the MITM tunnel to the
// real upstream, intercepting SSE responses with the OAI stream parser.
func (ch *ConnectHandler) forwardRequest(conn net.Conn, req *http.Request, hostname string) error {
	upstreamURL := fmt.Sprintf("https://%s%s", hostname, req.RequestURI)

	upReq, err := http.NewRequest(req.Method, upstreamURL, req.Body)
	if err != nil {
		return fmt.Errorf("building upstream request: %w", err)
	}

	for key, values := range req.Header {
		for _, v := range values {
			upReq.Header.Add(key, v)
		}
	}
	// Remove hop-by-hop headers
	upReq.Header.Del("Connection")
	upReq.Header.Del("Keep-Alive")
	upReq.Header.Del("Transfer-Encoding")
	// Prevent gzip so we can parse SSE for trap injection
	upReq.Header.Del("Accept-Encoding")

	resp, err := ch.upstreamHTTPClient.Do(upReq)
	if err != nil {
		errResp := &http.Response{
			StatusCode: http.StatusBadGateway,
			Status:     "502 Bad Gateway",
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Body:       io.NopCloser(strings.NewReader("proxy error: upstream request failed\n")),
			Header:     make(http.Header),
		}
		_ = errResp.Write(conn)
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		return ch.forwardSSEResponse(conn, resp)
	}

	return resp.Write(conn)
}

// forwardSSEResponse writes the SSE response headers to the tunnel connection,
// then pipes the body through the OAI stream interceptor line-by-line.
func (ch *ConnectHandler) forwardSSEResponse(conn net.Conn, resp *http.Response) error {
	writer := bufio.NewWriter(conn)

	// Write HTTP response status and headers
	fmt.Fprintf(writer, "HTTP/1.1 %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	for key, values := range resp.Header {
		for _, v := range values {
			fmt.Fprintf(writer, "%s: %s\r\n", key, v)
		}
	}
	fmt.Fprintf(writer, "\r\n")
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flushing SSE response headers: %w", err)
	}

	interceptor := NewOAIStreamInterceptor(
		ch.trapEngine,
		ch.trapSelector,
		ch.makeTrapInjectionFunc(),
		ch.logger,
	)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		outputLines, procErr := interceptor.ProcessLine(line)
		if procErr != nil {
			ch.logger.Error("OAI SSE processing error", "error", procErr)
			outputLines = []string{line}
		}
		for _, outLine := range outputLines {
			fmt.Fprintln(writer, outLine)
		}
		if flushErr := writer.Flush(); flushErr != nil {
			return fmt.Errorf("flushing SSE output: %w", flushErr)
		}
	}

	return scanner.Err()
}

// handlePlainTunnel proxies TCP bidirectionally for non-MITM hosts.
func (ch *ConnectHandler) handlePlainTunnel(conn net.Conn, targetAddr string) {
	defer conn.Close()

	upstreamConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
	if err != nil {
		ch.logger.Debug("plain tunnel: dial failed", "target", targetAddr, "error", err)
		return
	}
	defer upstreamConn.Close()

	errc := make(chan error, 1)
	go func() {
		_, err := io.Copy(upstreamConn, conn)
		errc <- err
	}()
	_, _ = io.Copy(conn, upstreamConn)
	<-errc
}

func (ch *ConnectHandler) makeTrapInjectionFunc() TrapInjectionFunc {
	return func(originalCmd string, tmpl *trap.Template, toolUseID string) string {
		activeTrap := ch.callbackHandler.RegisterTrap(originalCmd, tmpl, toolUseID)
		if activeTrap == nil {
			return ""
		}
		ch.logger.Info("OAI trap injected via CONNECT tunnel",
			"trap_id", activeTrap.ID,
			"tool_call_id", toolUseID,
			"template", tmpl.ID,
		)
		return activeTrap.TrapCommand
	}
}
