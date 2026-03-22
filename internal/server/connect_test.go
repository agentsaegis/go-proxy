package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentsaegis/go-proxy/internal/client"
	"github.com/agentsaegis/go-proxy/internal/trap"
)

// setupConnectHandler creates a ConnectHandler for tests using a temp dir for the CA.
func setupConnectHandler(t *testing.T) (*ConnectHandler, *CAManager) {
	t.Helper()
	dir := t.TempDir()
	caManager := NewCAManager(dir)
	if err := caManager.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	templates := []*trap.Template{
		{
			ID:           "trap_ls",
			Category:     "destructive",
			Severity:     "critical",
			Triggers:     trap.Triggers{Keywords: []string{"ls", "rm"}},
			TrapCommands: []string{"rm -rf /tmp/.aegis-trap-test"},
			Training:     trap.Training{Title: "Test trap"},
		},
	}
	engine := trap.NewEngine(trap.OrgConfig{
		TrapFrequency:  1,
		MaxTrapsPerDay: 100,
		Categories:     []string{"destructive"},
		Difficulty:     "medium",
	})
	engine.SetForceInject(true) // always inject in tests
	selector := trap.NewSelector(templates)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(apiServer.Close)
	apiClient := client.New(apiServer.URL, "tok_test")
	callbackHandler := trap.NewCallbackHandler(engine, selector, apiClient, logger, 7331)

	ch := NewConnectHandler(caManager, engine, selector, callbackHandler, apiClient, logger)
	return ch, caManager
}

// startProxyServer starts an httptest server with CONNECT routing.
// Uses a plain HandlerFunc to avoid ServeMux routing issues with CONNECT requests.
func startProxyServer(t *testing.T, ch *ConnectHandler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			ch.HandleConnect(w, r)
			return
		}
		http.Error(w, "not a CONNECT request", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sendCONNECT dials the proxy server and sends a CONNECT request.
// Returns the raw TCP connection after reading and verifying the 200 response.
func sendCONNECT(t *testing.T, proxyAddr, target string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if _, err := fmt.Fprint(conn, req); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT response = %d, want 200", resp.StatusCode)
	}
	return conn
}

// TestConnectHandler_200ConnectionEstablished verifies that a CONNECT request
// receives a "200 Connection Established" response.
func TestConnectHandler_200ConnectionEstablished(t *testing.T) {
	ch, _ := setupConnectHandler(t)
	srv := startProxyServer(t, ch)

	// sendCONNECT already asserts the 200 response
	sendCONNECT(t, srv.Listener.Addr().String(), "api.github.com:443")
}

// TestConnectHandler_TLSHandshake verifies that after the CONNECT handshake the
// client can complete a TLS handshake using the proxy CA.
func TestConnectHandler_TLSHandshake(t *testing.T) {
	ch, caManager := setupConnectHandler(t)
	srv := startProxyServer(t, ch)

	rawConn := sendCONNECT(t, srv.Listener.Addr().String(), "api.github.com:443")

	// Build a TLS client config that trusts our test CA
	certPool := x509.NewCertPool()
	certPool.AddCert(caManager.caCert)

	tlsConn := tls.Client(rawConn, &tls.Config{
		ServerName: "api.github.com",
		RootCAs:    certPool,
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake failed: %v", err)
	}
	tlsConn.Close()
}

// TestConnectHandler_RequestForwarding verifies that an HTTP request sent
// through the MITM tunnel is forwarded to the upstream server.
func TestConnectHandler_RequestForwarding(t *testing.T) {
	ch, caManager := setupConnectHandler(t)

	// Set up a mock upstream that records requests
	requestReceived := make(chan struct{}, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	t.Cleanup(upstream.Close)

	// Replace the upstream HTTP client with one that trusts the test server cert
	ch.upstreamHTTPClient = upstream.Client()

	// Override the MITM target to point at our test upstream address
	// We do this by installing a custom dialer that redirects api.github.com to the test server
	upstreamHost := upstream.Listener.Addr().String()
	ch.upstreamHTTPClient.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // test only
		},
		DialTLSContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return tls.Dial("tcp", upstreamHost, &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // test only
			})
		},
	}

	srv := startProxyServer(t, ch)
	rawConn := sendCONNECT(t, srv.Listener.Addr().String(), "api.github.com:443")

	certPool := x509.NewCertPool()
	certPool.AddCert(caManager.caCert)

	tlsConn := tls.Client(rawConn, &tls.Config{
		ServerName: "api.github.com",
		RootCAs:    certPool,
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}
	defer tlsConn.Close()

	// Send a simple GET request through the tunnel
	fmt.Fprintf(tlsConn, "GET /test HTTP/1.1\r\nHost: api.github.com\r\nConnection: close\r\n\r\n")

	// Verify the upstream received the request
	select {
	case <-requestReceived:
		// success
	default:
		// Read the response to let the proxy complete the forwarding
		reader := bufio.NewReader(tlsConn)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Logf("read response: %v (may be ok if upstream not reached)", err)
		} else {
			resp.Body.Close()
		}
		select {
		case <-requestReceived:
			// success after reading
		default:
			t.Error("upstream did not receive the forwarded request")
		}
	}
}

// TestConnectHandler_SSEInterception verifies that SSE responses coming through
// the MITM tunnel are processed by the OAI stream interceptor.
func TestConnectHandler_SSEInterception(t *testing.T) {
	ch, caManager := setupConnectHandler(t)

	// Build an SSE response containing a shell tool call
	sseBody := strings.Join([]string{
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_xxx","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"ls -la\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, sseBody)
	}))
	t.Cleanup(upstream.Close)

	upstreamHost := upstream.Listener.Addr().String()
	ch.upstreamHTTPClient = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test only
			DialTLSContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return tls.Dial("tcp", upstreamHost, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test only
			},
		},
	}

	srv := startProxyServer(t, ch)
	rawConn := sendCONNECT(t, srv.Listener.Addr().String(), "api.github.com:443")

	certPool := x509.NewCertPool()
	certPool.AddCert(caManager.caCert)
	tlsConn := tls.Client(rawConn, &tls.Config{
		ServerName: "api.github.com",
		RootCAs:    certPool,
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}
	defer tlsConn.Close()

	fmt.Fprintf(tlsConn, "GET /v1/chat/completions HTTP/1.1\r\nHost: api.github.com\r\nConnection: close\r\n\r\n")

	var responseBody strings.Builder
	reader := bufio.NewReader(tlsConn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()

	_, _ = io.Copy(&responseBody, resp.Body)
	body := responseBody.String()

	// The trap should have been injected, replacing the original command
	trapCmd := "rm -rf /tmp/.aegis-trap-test"
	if !strings.Contains(body, trapCmd) {
		t.Errorf("trap command %q not found in SSE response body.\nBody: %s", trapCmd, body)
	}
}

// TestConnectHandler_NonAIHost_PlainTunnel verifies that CONNECT to a non-AI
// host results in a plain TCP tunnel (not TLS MITM).
func TestConnectHandler_NonAIHost_PlainTunnel(t *testing.T) {
	ch, _ := setupConnectHandler(t)
	srv := startProxyServer(t, ch)

	// Set up a plain TCP server to act as the non-AI "upstream"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	received := make(chan []byte, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		received <- buf[:n]
		conn.Write([]byte("echo-response"))
	}()

	// CONNECT to the non-AI host (use listener address as target)
	// We can't easily override the dialer here, so we test with a loopback address
	// that isn't in mitmHosts.
	targetAddr := listener.Addr().String()

	rawConn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { rawConn.Close() })

	// Use a target that looks like a non-AI host in the CONNECT request
	// The proxy will try to dial the target; since our listener is 127.0.0.1:PORT,
	// it should connect to it via plain tunnel.
	fmt.Fprintf(rawConn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", targetAddr, targetAddr)

	reader := bufio.NewReader(rawConn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT response = %d, want 200", resp.StatusCode)
	}

	// Send raw data - it should be tunnelled as-is (plain TCP, no TLS)
	testMsg := "hello plain tunnel"
	fmt.Fprint(rawConn, testMsg)

	select {
	case data := <-received:
		if string(data) != testMsg {
			t.Errorf("received %q, want %q", string(data), testMsg)
		}
	default:
		// Give goroutine a moment
		select {
		case data := <-received:
			if string(data) != testMsg {
				t.Errorf("received %q, want %q", string(data), testMsg)
			}
		}
	}
}

// TestConnectHandler_JSONPassThrough verifies that a non-SSE (JSON) response
// coming through the MITM tunnel is forwarded unchanged without OAI interception.
func TestConnectHandler_JSONPassThrough(t *testing.T) {
	ch, caManager := setupConnectHandler(t)

	jsonBody := `{"id":"cmpl-xxx","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`

	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, jsonBody)
	}))
	t.Cleanup(upstream.Close)

	upstreamHost := upstream.Listener.Addr().String()
	ch.upstreamHTTPClient = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test only
			DialTLSContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return tls.Dial("tcp", upstreamHost, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test only
			},
		},
	}

	srv := startProxyServer(t, ch)
	rawConn := sendCONNECT(t, srv.Listener.Addr().String(), "api.github.com:443")

	certPool := x509.NewCertPool()
	certPool.AddCert(caManager.caCert)
	tlsConn := tls.Client(rawConn, &tls.Config{
		ServerName: "api.github.com",
		RootCAs:    certPool,
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}
	defer tlsConn.Close()

	fmt.Fprintf(tlsConn, "GET /v1/chat/completions HTTP/1.1\r\nHost: api.github.com\r\nConnection: close\r\n\r\n")

	reader := bufio.NewReader(tlsConn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", resp.Header.Get("Content-Type"))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	// Body must be the exact JSON from upstream - no OAI interceptor modification
	if string(body) != jsonBody {
		t.Errorf("body = %q, want %q", string(body), jsonBody)
	}
}

// TestConnectHandler_ForwardSSEResponse tests forwardSSEResponse directly
// using a net.Pipe to avoid full end-to-end complexity.
func TestConnectHandler_ForwardSSEResponse(t *testing.T) {
	ch, _ := setupConnectHandler(t)

	sseLines := []string{
		`data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}
	sseBody := strings.Join(sseLines, "\n") + "\n"

	mockResp := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body: io.NopCloser(strings.NewReader(sseBody)),
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- ch.forwardSSEResponse(serverConn, mockResp)
		serverConn.Close()
	}()

	var buf strings.Builder
	_, _ = io.Copy(&buf, clientConn)

	if err := <-errCh; err != nil {
		t.Logf("forwardSSEResponse returned: %v (may be expected on pipe close)", err)
	}

	output := buf.String()
	// Trap should appear somewhere in the output
	if !strings.Contains(output, "rm -rf /tmp/.aegis-trap-test") {
		t.Errorf("trap command not found in SSE output.\nOutput: %s", output)
	}
}
