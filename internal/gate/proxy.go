// Copyright 2026 Brian Bouterse
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gate

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bmbouter/alcove/internal"
)

// DomainAllowlist contains hosts that are allowed for general HTTP access
// (e.g., package registries for pip/npm install).
var DomainAllowlist = []string{
	"pypi.org",
	"files.pythonhosted.org",
	"registry.npmjs.org",
	"crates.io",
	"static.crates.io",
	"proxy.golang.org",
	"sum.golang.org",
	"storage.googleapis.com",
	"dl-cdn.alpinelinux.org",
	"download.docker.com",
	"rubygems.org",
	"repo.maven.apache.org",
}

// ToolConfig describes how Gate should proxy requests for a specific tool.
type ToolConfig struct {
	APIHost    string `json:"api_host"`
	AuthHeader string `json:"auth_header"`
	AuthFormat string `json:"auth_format"` // "bearer", "header", "basic"
}

// Config holds all configuration for a Gate proxy instance.
type Config struct {
	SessionID          string
	Scope              internal.Scope
	Credentials        map[string]string       // service name -> real credential
	ToolConfigs        map[string]ToolConfig    // tool name -> proxy config
	SessionToken       string                  // opaque token that Skiff presents
	LLMToken           string                  // bearer token or API key (was LLMAPIKey)
	LLMProvider        string                  // "anthropic" or "google-vertex"
	LLMTokenType       string                  // "api_key" or "bearer"
	TokenRefreshURL    string                  // Bridge endpoint for token refresh
	TokenRefreshSecret string                  // session-scoped secret for refresh auth
	VertexRegion       string // Vertex AI region (e.g., "us-east5")
	VertexProject      string // Vertex AI project ID
	LedgerURL          string
	GitLabHost         string // self-hosted GitLab hostname (default: "gitlab.com")
}

// Proxy is the Gate authorization proxy.
type Proxy struct {
	config Config

	mu       sync.Mutex
	logBuf   []internal.ProxyLogEntry
	stopChan chan struct{}
}

// NewProxy creates a new Gate proxy with the given configuration.
func NewProxy(cfg Config) *Proxy {
	return &Proxy{
		config:   cfg,
		stopChan: make(chan struct{}),
	}
}

// Handler returns an http.Handler that implements the Gate proxy.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()

	// Git credential helper endpoint
	mux.HandleFunc("/git-credential", func(w http.ResponseWriter, r *http.Request) {
		HandleGitCredential(w, r, p.config.Scope, p.config.Credentials)
		p.logEntry(r.Method, r.URL.String(), "gate", "git_credential", "allow", http.StatusOK)
	})

	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	// Register dynamic tool proxy endpoints from ToolConfigs
	for toolName := range p.config.ToolConfigs {
		name := toolName // capture for closure
		mux.HandleFunc("/"+name+"/", func(w http.ResponseWriter, r *http.Request) {
			p.handleSCMProxy(w, r, name)
		})
	}

	// Keep hardcoded /github/ and /gitlab/ as fallbacks if not in ToolConfigs
	if _, ok := p.config.ToolConfigs["github"]; !ok {
		mux.HandleFunc("/github/", func(w http.ResponseWriter, r *http.Request) {
			p.handleSCMProxy(w, r, "github")
		})
	}
	if _, ok := p.config.ToolConfigs["gitlab"]; !ok {
		mux.HandleFunc("/gitlab/", func(w http.ResponseWriter, r *http.Request) {
			p.handleSCMProxy(w, r, "gitlab")
		})
	}

	// LLM API proxy -- handles requests when ANTHROPIC_BASE_URL or similar
	// is set to http://localhost:8443. These arrive as normal HTTP requests
	// (not CONNECT) because the client is configured to talk to Gate directly.
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		p.handleLLMRequest(w, r)
	})

	// Default handler -- serves as HTTP proxy for CONNECT tunneling
	// and plain HTTP proxy requests.
	return &proxyHandler{
		proxy: p,
		mux:   mux,
	}
}

// proxyHandler dispatches between the mux (for direct requests) and
// CONNECT tunneling (for HTTPS proxy requests).
type proxyHandler struct {
	proxy *Proxy
	mux   *http.ServeMux
}

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CONNECT method = HTTPS tunnel request
	if r.Method == http.MethodConnect {
		h.proxy.handleConnect(w, r)
		return
	}

	// If the request has an absolute URL (proxy-style request), handle as proxy
	if r.URL.IsAbs() {
		h.proxy.handleProxyRequest(w, r)
		return
	}

	// Otherwise, serve via the mux (git-credential, healthz, LLM /v1/)
	h.mux.ServeHTTP(w, r)
}

// handleConnect processes HTTP CONNECT requests for HTTPS tunneling.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}

	hostname := hostOnly(host)

	// Determine request category
	switch {
	case isLLMHost(hostname):
		p.tunnelToLLM(w, r, host)
	case hostname == "api.github.com":
		// Block CONNECT tunnels to api.github.com -- API operations must go
		// through the /github/ proxy endpoint for operation-level enforcement.
		http.Error(w, "Forbidden: use /github/ proxy endpoint for API calls", http.StatusForbidden)
		p.logEntry("CONNECT", host, "github", "", "deny", http.StatusForbidden)
	case isServiceHost(hostname):
		p.tunnelToService(w, r, host, hostname)
	case isDomainAllowed(hostname):
		p.tunnelDirect(w, r, host)
		p.logEntry("CONNECT", host, "allowlist", "passthrough", "allow", http.StatusOK)
	default:
		http.Error(w, "Forbidden: host not allowed", http.StatusForbidden)
		p.logEntry("CONNECT", host, "unknown", "", "deny", http.StatusForbidden)
	}
}

// handleProxyRequest handles plain HTTP proxy requests (non-CONNECT).
func (p *Proxy) handleProxyRequest(w http.ResponseWriter, r *http.Request) {
	hostname := hostOnly(r.URL.Host)

	if isLLMHost(hostname) {
		p.handleLLMForward(w, r)
		return
	}

	if isServiceHost(hostname) {
		p.handleServiceForward(w, r)
		return
	}

	if isDomainAllowed(hostname) {
		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL = r.URL
				req.Host = r.URL.Host
			},
		}
		proxy.ServeHTTP(w, r)
		p.logEntry(r.Method, r.URL.String(), "allowlist", "passthrough", "allow", http.StatusOK)
		return
	}

	http.Error(w, "Forbidden: host not allowed", http.StatusForbidden)
	p.logEntry(r.Method, r.URL.String(), "unknown", "", "deny", http.StatusForbidden)
}

// handleLLMRequest handles LLM API calls that arrive as direct HTTP requests
// (when ANTHROPIC_BASE_URL=http://localhost:8443).
func (p *Proxy) handleLLMRequest(w http.ResponseWriter, r *http.Request) {
	var targetURL string
	switch p.config.LLMProvider {
	case "anthropic":
		targetURL = "https://api.anthropic.com" + r.URL.Path
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}
	case "google-vertex":
		region := p.config.VertexRegion
		if region == "" {
			region = "us-east5"
		}
		project := p.config.VertexProject

		// Read the body to extract model and stream flag.
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadGateway)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		var reqBody struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.Unmarshal(bodyBytes, &reqBody)

		model := reqBody.Model
		if model == "" {
			model = "claude-sonnet-4@20250514"
		}

		// Convert Anthropic model name format to Vertex format:
		// "claude-sonnet-4-20250514" → "claude-sonnet-4@20250514"
		// Find the last hyphen followed by a date-like pattern and replace with @
		if idx := strings.LastIndex(model, "-20"); idx > 0 && !strings.Contains(model, "@") {
			model = model[:idx] + "@" + model[idx+1:]
		}

		// Choose rawPredict vs streamRawPredict based on stream flag.
		method := "rawPredict"
		if reqBody.Stream {
			method = "streamRawPredict"
		}

		// Transform body for Vertex AI:
		// - Remove "model" (it's in the URL path)
		// - Remove fields unsupported by Vertex rawPredict
		// - Add "anthropic_version" (required by Vertex rawPredict)
		var bodyMap map[string]any
		if json.Unmarshal(bodyBytes, &bodyMap) == nil {
			delete(bodyMap, "model")
			delete(bodyMap, "context_management")
			if _, ok := bodyMap["anthropic_version"]; !ok {
				bodyMap["anthropic_version"] = "vertex-2023-10-16"
			}
			if newBody, err := json.Marshal(bodyMap); err == nil {
				bodyBytes = newBody
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				r.ContentLength = int64(len(bodyBytes))
			}
		}

		targetURL = fmt.Sprintf(
			"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:%s",
			region, project, region, model, method,
		)
	default:
		http.Error(w, "unknown LLM provider", http.StatusInternalServerError)
		return
	}

	target, _ := url.Parse(targetURL)

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL = target
			req.Host = target.Host

			// Inject credential based on token type
			switch p.config.LLMTokenType {
			case "bearer":
				req.Header.Set("Authorization", "Bearer "+p.config.LLMToken)
			case "api_key":
				if p.config.LLMProvider == "anthropic" {
					req.Header.Set("x-api-key", p.config.LLMToken)
					req.Header.Set("anthropic-version", "2023-06-01")
				} else {
					req.Header.Set("Authorization", "Bearer "+p.config.LLMToken)
				}
			default:
				// Legacy fallback
				req.Header.Set("x-api-key", p.config.LLMToken)
			}

			// For Vertex AI, ensure the modified body is used and strip
			// unsupported headers.
			if p.config.LLMProvider == "google-vertex" {
				req.Body = r.Body
				req.ContentLength = r.ContentLength
				req.Header.Del("anthropic-beta")
				req.Header.Del("anthropic-version")
				req.Header.Del("x-api-key")
			}
		},
		// Enable streaming: flush SSE chunks immediately.
		FlushInterval: -1,
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode == http.StatusUnauthorized {
			log.Println("gate: LLM returned 401, attempting token refresh")
			if err := p.refreshToken(); err != nil {
				log.Printf("gate: token refresh failed: %v", err)
			}
		}
		return nil
	}

	proxy.ServeHTTP(w, r)
	p.logEntry(r.Method, r.URL.Path, "llm", p.config.LLMProvider, "allow", http.StatusOK)
}

// handleLLMForward forwards an LLM request that came through the proxy (absolute URL).
func (p *Proxy) handleLLMForward(w http.ResponseWriter, r *http.Request) {
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL = r.URL
			req.Host = r.URL.Host

			// Inject credential based on token type
			switch p.config.LLMTokenType {
			case "bearer":
				req.Header.Set("Authorization", "Bearer "+p.config.LLMToken)
			case "api_key":
				if p.config.LLMProvider == "anthropic" {
					req.Header.Set("x-api-key", p.config.LLMToken)
					req.Header.Set("anthropic-version", "2023-06-01")
				} else {
					req.Header.Set("Authorization", "Bearer "+p.config.LLMToken)
				}
			default:
				// Legacy fallback
				req.Header.Set("x-api-key", p.config.LLMToken)
			}
		},
	}
	proxy.ServeHTTP(w, r)
	p.logEntry(r.Method, r.URL.String(), "llm", p.config.LLMProvider, "allow", http.StatusOK)
}

// handleServiceForward handles service API requests arriving as plain HTTP proxy requests.
func (p *Proxy) handleServiceForward(w http.ResponseWriter, r *http.Request) {
	result := CheckAccess(r.Method, r.URL.String(), p.config.Scope)
	if !result.Allowed {
		http.Error(w, "Forbidden: "+result.Reason, http.StatusForbidden)
		p.logEntry(r.Method, r.URL.String(), result.Service, result.Operation, "deny", http.StatusForbidden)
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL = r.URL
			req.Host = r.URL.Host
			p.injectServiceCredential(req, result.Service)
		},
	}
	proxy.ServeHTTP(w, r)
	p.logEntry(r.Method, r.URL.String(), result.Service, result.Operation, "allow", http.StatusOK)
}

// handleSCMProxy handles API calls arriving at proxy endpoints like /github/,
// /gitlab/, or any dynamically registered tool endpoint. It strips the service
// prefix, checks access against the scope, injects real credentials, and
// reverse-proxies to the real API.
func (p *Proxy) handleSCMProxy(w http.ResponseWriter, r *http.Request, service string) {
	prefix := "/" + service + "/"
	apiPath := strings.TrimPrefix(r.URL.Path, prefix)
	if apiPath == "" {
		apiPath = "/"
	}

	// Look up tool config for target host and auth settings
	var targetHost, authHeader, authFormat string
	if tc, ok := p.config.ToolConfigs[service]; ok {
		targetHost = tc.APIHost
		authHeader = tc.AuthHeader
		authFormat = tc.AuthFormat
	} else {
		// Fallback to hardcoded defaults for backward compat
		switch service {
		case "github":
			targetHost = "api.github.com"
			authHeader = "Authorization"
			authFormat = "bearer"
		case "gitlab":
			targetHost = p.config.GitLabHost
			if targetHost == "" {
				targetHost = "gitlab.com"
			}
			authHeader = "PRIVATE-TOKEN"
			authFormat = "header"
		default:
			http.Error(w, "unknown tool: "+service, http.StatusNotFound)
			return
		}
	}

	fakeURL := fmt.Sprintf("https://%s/%s", targetHost, apiPath)
	if r.URL.RawQuery != "" {
		fakeURL += "?" + r.URL.RawQuery
	}

	result := CheckAccess(r.Method, fakeURL, p.config.Scope)
	if !result.Allowed {
		http.Error(w, "Forbidden: "+result.Reason, http.StatusForbidden)
		p.logEntry(r.Method, fakeURL, result.Service, result.Operation, "deny", http.StatusForbidden)
		return
	}

	targetURL, _ := url.Parse(fakeURL)

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL = targetURL
			req.Host = targetHost
			p.injectToolCredential(req, service, authHeader, authFormat)
		},
		FlushInterval: -1,
	}
	proxy.ServeHTTP(w, r)
	p.logEntry(r.Method, fakeURL, result.Service, result.Operation, "allow", http.StatusOK)
}

// tunnelToLLM handles CONNECT tunnels for LLM API hosts.
// Since the design prefers ANTHROPIC_BASE_URL=http://localhost:8443 for
// header injection, CONNECT-based LLM calls are tunneled as passthrough.
func (p *Proxy) tunnelToLLM(w http.ResponseWriter, r *http.Request, targetHost string) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		log.Printf("gate: hijack failed: %v", err)
		return
	}
	defer clientConn.Close()

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// Connect to the real upstream over TLS
	upstreamConn, err := tls.Dial("tcp", ensurePort(targetHost, "443"), &tls.Config{
		ServerName: hostOnly(targetHost),
	})
	if err != nil {
		log.Printf("gate: upstream TLS dial failed for %s: %v", targetHost, err)
		return
	}
	defer upstreamConn.Close()

	p.logEntry("CONNECT", targetHost, "llm", p.config.LLMProvider, "allow", http.StatusOK)
	bidirectionalCopy(clientConn, clientBuf, upstreamConn)
}

// tunnelToService handles CONNECT tunnels for service APIs (GitHub, GitLab, etc.).
// CONNECT tunnels only allow domain-level enforcement since we cannot inspect
// or modify the encrypted payload without MITM TLS.
func (p *Proxy) tunnelToService(w http.ResponseWriter, r *http.Request, targetHost, hostname string) {
	service := identifyService(hostname)
	if _, ok := p.config.Scope.Services[service]; !ok {
		http.Error(w, "Forbidden: service not in scope", http.StatusForbidden)
		p.logEntry("CONNECT", targetHost, service, "", "deny", http.StatusForbidden)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		log.Printf("gate: hijack failed: %v", err)
		return
	}
	defer clientConn.Close()

	upstreamConn, err := net.DialTimeout("tcp", ensurePort(targetHost, "443"), 10*time.Second)
	if err != nil {
		log.Printf("gate: upstream dial failed for %s: %v", targetHost, err)
		return
	}
	defer upstreamConn.Close()

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	p.logEntry("CONNECT", targetHost, service, "tunnel", "allow", http.StatusOK)
	bidirectionalCopy(clientConn, clientBuf, upstreamConn)
}

// tunnelDirect creates a passthrough tunnel for allowed domains.
func (p *Proxy) tunnelDirect(w http.ResponseWriter, r *http.Request, targetHost string) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		log.Printf("gate: hijack failed: %v", err)
		return
	}
	defer clientConn.Close()

	upstreamConn, err := net.DialTimeout("tcp", ensurePort(targetHost, "443"), 10*time.Second)
	if err != nil {
		log.Printf("gate: upstream dial failed for %s: %v", targetHost, err)
		return
	}
	defer upstreamConn.Close()

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	bidirectionalCopy(clientConn, clientBuf, upstreamConn)
}

// bidirectionalCopy pipes data between two connections, draining any buffered
// data from the hijacked connection first.
func bidirectionalCopy(client net.Conn, clientBuf *bufio.ReadWriter, upstream net.Conn) {
	done := make(chan struct{}, 2)

	go func() {
		// Flush any buffered data from the client first
		if clientBuf != nil && clientBuf.Reader.Buffered() > 0 {
			buf := make([]byte, clientBuf.Reader.Buffered())
			n, _ := clientBuf.Read(buf)
			if n > 0 {
				_, _ = upstream.Write(buf[:n])
			}
		}
		_, _ = io.Copy(upstream, client)
		done <- struct{}{}
	}()

	go func() {
		_, _ = io.Copy(client, upstream)
		done <- struct{}{}
	}()

	<-done
}

// injectToolCredential replaces the session token with real credentials
// using the auth format specified in the tool config.
func (p *Proxy) injectToolCredential(req *http.Request, service, authHeader, authFormat string) {
	cred, ok := p.config.Credentials[service]
	if !ok || cred == "" {
		return
	}

	switch authFormat {
	case "bearer":
		req.Header.Set(authHeader, "Bearer "+cred)
	case "header":
		req.Header.Set(authHeader, cred)
	case "basic":
		req.Header.Set(authHeader, "Basic "+base64.StdEncoding.EncodeToString([]byte(cred)))
	default:
		req.Header.Set(authHeader, "Bearer "+cred)
	}
}

// injectServiceCredential replaces the session token with real credentials.
// Deprecated: use injectToolCredential. Kept for backward compatibility with
// handleServiceForward which uses the legacy code path.
func (p *Proxy) injectServiceCredential(req *http.Request, service string) {
	// Try ToolConfigs first
	if tc, ok := p.config.ToolConfigs[service]; ok {
		p.injectToolCredential(req, service, tc.AuthHeader, tc.AuthFormat)
		return
	}

	// Fallback to hardcoded defaults
	cred, ok := p.config.Credentials[service]
	if !ok {
		return
	}

	switch service {
	case "github":
		req.Header.Set("Authorization", "Bearer "+cred)
	case "gitlab":
		req.Header.Set("PRIVATE-TOKEN", cred)
	case "jira":
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cred)))
	}
}

// logEntry records a proxy log entry.
func (p *Proxy) logEntry(method, rawURL, service, operation, decision string, statusCode int) {
	entry := internal.ProxyLogEntry{
		Timestamp:  time.Now(),
		Method:     method,
		URL:        rawURL,
		Service:    service,
		Operation:  operation,
		Decision:   decision,
		StatusCode: statusCode,
		SessionID:  p.config.SessionID,
	}

	p.mu.Lock()
	p.logBuf = append(p.logBuf, entry)
	p.mu.Unlock()
}

// FlushLogs returns and clears the accumulated proxy log entries.
func (p *Proxy) FlushLogs() []internal.ProxyLogEntry {
	p.mu.Lock()
	defer p.mu.Unlock()

	entries := p.logBuf
	p.logBuf = nil
	return entries
}

// StartLogFlusher starts a goroutine that periodically flushes logs to Ledger.
func (p *Proxy) StartLogFlusher(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				entries := p.FlushLogs()
				if len(entries) > 0 {
					p.SendLogs(entries)
				}
			case <-p.stopChan:
				// Final flush
				entries := p.FlushLogs()
				if len(entries) > 0 {
					p.SendLogs(entries)
				}
				return
			}
		}
	}()
}

// Stop signals the proxy to stop its background goroutines and flush remaining logs.
func (p *Proxy) Stop() {
	close(p.stopChan)
}

// SendLogs sends proxy log entries to the Ledger service.
func (p *Proxy) SendLogs(entries []internal.ProxyLogEntry) {
	if p.config.LedgerURL == "" {
		log.Printf("gate: GATE_LEDGER_URL is empty — cannot send %d proxy log entries", len(entries))
		return
	}

	url := p.config.LedgerURL + "/api/v1/sessions/" + p.config.SessionID + "/proxy-log"
	payload := struct {
		Entries []internal.ProxyLogEntry `json:"entries"`
	}{Entries: entries}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("gate: failed to marshal proxy log: %v", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		log.Printf("gate: failed to create proxy log request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if p.config.SessionToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.config.SessionToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("gate: failed to send proxy log to %s: %v", url, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		log.Printf("gate: ledger proxy-log POST to %s returned HTTP %d: %s", url, resp.StatusCode, string(body))
	} else {
		log.Printf("gate: flushed %d proxy log entries to ledger", len(entries))
	}
}

func (p *Proxy) refreshToken() error {
	if p.config.TokenRefreshURL == "" {
		return fmt.Errorf("no token refresh URL configured")
	}
	reqBody, _ := json.Marshal(map[string]string{
		"session_id":     p.config.SessionID,
		"refresh_secret": p.config.TokenRefreshSecret,
	})
	resp, err := http.Post(p.config.TokenRefreshURL, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh returned %d", resp.StatusCode)
	}
	var result struct {
		Token     string `json:"token"`
		TokenType string `json:"token_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding refresh response: %w", err)
	}
	p.mu.Lock()
	p.config.LLMToken = result.Token
	if result.TokenType != "" {
		p.config.LLMTokenType = result.TokenType
	}
	p.mu.Unlock()
	log.Println("gate: LLM token refreshed successfully")
	return nil
}

// isLLMHost returns true if the host is an LLM API endpoint.
func isLLMHost(host string) bool {
	return host == "api.anthropic.com" ||
		strings.HasSuffix(host, ".googleapis.com")
}

// isServiceHost returns true if the host is a known service API.
func isServiceHost(host string) bool {
	return host == "api.github.com" ||
		strings.Contains(host, "github.com") ||
		strings.Contains(host, "gitlab") ||
		strings.HasSuffix(host, ".atlassian.net")
}

// identifyService returns the service name for a given hostname.
func identifyService(host string) string {
	switch {
	case strings.Contains(host, "github"):
		return "github"
	case strings.Contains(host, "gitlab"):
		return "gitlab"
	case strings.HasSuffix(host, ".atlassian.net"):
		return "jira"
	default:
		return "unknown"
	}
}

// isDomainAllowed checks if a hostname is on the domain allowlist.
func isDomainAllowed(host string) bool {
	for _, d := range DomainAllowlist {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// hostOnly strips the port from a host:port string.
func hostOnly(hostPort string) string {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return hostPort
	}
	return host
}

// ensurePort adds a default port if none is present.
func ensurePort(hostPort, defaultPort string) string {
	_, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return net.JoinHostPort(hostPort, defaultPort)
	}
	return hostPort
}
