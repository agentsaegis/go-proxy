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
// errSSEPassthrough is returned by forwardRequest when a non-intercepted SSE
// stream has been handed off to a background goroutine. The MITM tunnel loop
// should stop reading further requests on this connection.
var errSSEPassthrough = fmt.Errorf("SSE passthrough active")

var mitmHosts = map[string]bool{
	"api.github.com":                    true,
	"api.individual.githubcopilot.com":  true,
	"api.business.githubcopilot.com":    true,
	"api.enterprise.githubcopilot.com":  true,
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
				// Force HTTP/1.1: the MITM tunnel writes HTTP/1.1 back to the
				// client, so the upstream connection must also be HTTP/1.1 to
				// avoid HTTP/2 framing mismatches (GOAWAY errors).
				ForceAttemptHTTP2: false,
				TLSNextProto:     make(map[string]func(string, *tls.Conn) http.RoundTripper),
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
			if fwdErr == errSSEPassthrough {
				// SSE body is being streamed by a background goroutine.
				// Stop the request loop but don't close the connection -
				// the goroutine owns it now and will write until done.
				ch.logger.Debug("SSE passthrough active, handing off connection", "hostname", hostname)
				// Block until the connection is closed by the remote end
				// (read will return when the client disconnects).
				buf := make([]byte, 1)
				for {
					if _, err := tlsConn.Read(buf); err != nil {
						return
					}
				}
			}
			ch.logger.Debug("forwarding tunneled request failed", "hostname", hostname, "error", fwdErr)
			return
		}
		// Close after responding to a Connection: close request
		if req.Close {
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

	ch.logger.Debug("MITM forwarding request", "method", req.Method, "url", upstreamURL)
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
	ch.logger.Debug("MITM upstream response", "status", resp.StatusCode, "content_type", contentType, "url", upstreamURL)

	// Only intercept chat/completions SSE for trap injection.
	// Other SSE endpoints (like /mcp/readonly) must pass through unchanged.
	isChatSSE := strings.Contains(contentType, "text/event-stream") &&
		strings.Contains(req.RequestURI, "/chat/completions")
	if isChatSSE {
		ch.logger.Info("MITM SSE stream detected, intercepting", "url", upstreamURL)
		return ch.forwardSSEResponse(conn, resp)
	}

	// For non-intercepted SSE streams (like /mcp/readonly), write headers
	// then stream the body without blocking the request loop. resp.Write()
	// would block until the SSE stream ends, preventing subsequent requests
	// on this connection.
	if strings.Contains(contentType, "text/event-stream") {
		ch.logger.Debug("MITM passthrough SSE (non-blocking)", "url", upstreamURL)
		// Write headers
		var headerBuf strings.Builder
		fmt.Fprintf(&headerBuf, "HTTP/1.1 %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode))
		for key, values := range resp.Header {
			for _, v := range values {
				fmt.Fprintf(&headerBuf, "%s: %s\r\n", key, v)
			}
		}
		headerBuf.WriteString("\r\n")
		if _, err := conn.Write([]byte(headerBuf.String())); err != nil {
			return fmt.Errorf("writing SSE passthrough headers: %w", err)
		}
		// Stream body in background - this SSE may be long-lived
		go func() {
			defer resp.Body.Close()
			io.Copy(conn, resp.Body)
		}()
		// Return a sentinel error to break out of the request loop for this
		// connection since we handed off the body to a goroutine.
		return errSSEPassthrough
	}

	return resp.Write(conn)
}

// forwardSSEResponse writes the SSE response headers to the tunnel connection,
// then pipes the body through the OAI stream interceptor line-by-line using
// chunked transfer encoding so the client knows when the body ends.
func (ch *ConnectHandler) forwardSSEResponse(conn net.Conn, resp *http.Response) error {
	writer := bufio.NewWriter(conn)

	// Write HTTP response status and headers.
	// Replace Transfer-Encoding/Content-Length with chunked encoding so the
	// client can detect end-of-body via the zero-length terminating chunk,
	// without requiring a connection close.
	fmt.Fprintf(writer, "HTTP/1.1 %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	for key, values := range resp.Header {
		lower := strings.ToLower(key)
		if lower == "transfer-encoding" || lower == "content-length" {
			continue
		}
		for _, v := range values {
			fmt.Fprintf(writer, "%s: %s\r\n", key, v)
		}
	}
	fmt.Fprintf(writer, "Transfer-Encoding: chunked\r\n")
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

	lineCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineCount++
		outputLines, procErr := interceptor.ProcessLine(line)
		if procErr != nil {
			ch.logger.Error("OAI SSE processing error", "error", procErr)
			outputLines = []string{line}
		}
		if lineCount <= 10 || lineCount%50 == 0 {
			preview := line
			if len(preview) > 300 {
				preview = preview[:300]
			}
			ch.logger.Debug("OAI SSE line", "n", lineCount, "in_len", len(line), "out_count", len(outputLines), "preview", preview)
		}
		for _, outLine := range outputLines {
			chunk := outLine + "\n"
			fmt.Fprintf(writer, "%x\r\n%s\r\n", len(chunk), chunk)
		}
		if flushErr := writer.Flush(); flushErr != nil {
			return fmt.Errorf("flushing SSE output: %w", flushErr)
		}
	}
	ch.logger.Debug("OAI SSE stream ended", "total_lines", lineCount, "scan_err", scanner.Err())

	// Write the terminating zero-length chunk to signal end of body
	fmt.Fprintf(writer, "0\r\n\r\n")
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flushing SSE terminating chunk: %w", err)
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
