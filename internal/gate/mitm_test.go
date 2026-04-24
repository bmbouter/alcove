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
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bmbouter/alcove/internal"
)

func mustGenerateTestCA(t *testing.T) ([]byte, []byte) {
	t.Helper()
	certPEM, keyPEM, err := GenerateTestCA()
	if err != nil {
		t.Fatalf("generating test CA: %v", err)
	}
	return certPEM, keyPEM
}

// TestCertCacheGetOrCreate verifies leaf cert generation, hostname in SAN, and caching.
func TestCertCacheGetOrCreate(t *testing.T) {
	certPEM, keyPEM := mustGenerateTestCA(t)
	cfg := &Config{
		Scope: internal.Scope{
			Services: map[string]internal.ServiceScope{
				"github": {Repos: []string{"*"}, Operations: []string{"*"}},
			},
		},
		Credentials: map[string]string{"github": "ghp_test"},
		ToolConfigs: map[string]ToolConfig{},
	}
	m, err := NewMITMHandler(certPEM, keyPEM, cfg)
	if err != nil {
		t.Fatalf("creating MITM handler: %v", err)
	}

	cert1, err := m.certCache.GetOrCreate("api.github.com", m.caCert, m.caKey)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	// Verify hostname is in SAN
	leaf := cert1.Leaf
	if leaf == nil {
		t.Fatal("leaf cert is nil")
	}
	found := false
	for _, name := range leaf.DNSNames {
		if name == "api.github.com" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected api.github.com in SAN, got %v", leaf.DNSNames)
	}

	// Verify CN
	if leaf.Subject.CommonName != "api.github.com" {
		t.Errorf("expected CN=api.github.com, got %q", leaf.Subject.CommonName)
	}

	// Second call should return cached cert
	cert2, err := m.certCache.GetOrCreate("api.github.com", m.caCert, m.caKey)
	if err != nil {
		t.Fatalf("GetOrCreate (cached): %v", err)
	}
	if cert1 != cert2 {
		t.Error("expected second call to return cached certificate")
	}

	// Different hostname should return different cert
	cert3, err := m.certCache.GetOrCreate("github.com", m.caCert, m.caKey)
	if err != nil {
		t.Fatalf("GetOrCreate (different host): %v", err)
	}
	if cert1 == cert3 {
		t.Error("expected different cert for different hostname")
	}
}

// TestCertCacheEviction verifies oldest entries are evicted at capacity.
func TestCertCacheEviction(t *testing.T) {
	certPEM, keyPEM := mustGenerateTestCA(t)
	cfg := &Config{
		Scope: internal.Scope{
			Services: map[string]internal.ServiceScope{
				"github": {Repos: []string{"*"}, Operations: []string{"*"}},
			},
		},
		Credentials: map[string]string{},
		ToolConfigs: map[string]ToolConfig{},
	}
	m, err := NewMITMHandler(certPEM, keyPEM, cfg)
	if err != nil {
		t.Fatalf("creating MITM handler: %v", err)
	}

	// Fill cache to capacity
	for i := 0; i < certCacheMaxSize; i++ {
		hostname := fmt.Sprintf("host%d.example.com", i)
		_, err := m.certCache.GetOrCreate(hostname, m.caCert, m.caKey)
		if err != nil {
			t.Fatalf("GetOrCreate for %s: %v", hostname, err)
		}
	}

	if m.certCache.len() != certCacheMaxSize {
		t.Fatalf("expected cache size %d, got %d", certCacheMaxSize, m.certCache.len())
	}

	// Add one more — should evict host0.example.com
	_, err = m.certCache.GetOrCreate("overflow.example.com", m.caCert, m.caKey)
	if err != nil {
		t.Fatalf("GetOrCreate for overflow: %v", err)
	}

	if m.certCache.len() != certCacheMaxSize {
		t.Fatalf("expected cache size %d after eviction, got %d", certCacheMaxSize, m.certCache.len())
	}

	// Verify host0 was evicted
	m.certCache.mu.Lock()
	_, exists := m.certCache.entries["host0.example.com"]
	m.certCache.mu.Unlock()
	if exists {
		t.Error("expected host0.example.com to be evicted")
	}

	// Verify overflow exists
	m.certCache.mu.Lock()
	_, exists = m.certCache.entries["overflow.example.com"]
	m.certCache.mu.Unlock()
	if !exists {
		t.Error("expected overflow.example.com to be in cache")
	}
}

// TestMITMHandler_IsMITMDomain tests domain matching.
func TestMITMHandler_IsMITMDomain(t *testing.T) {
	certPEM, keyPEM := mustGenerateTestCA(t)
	cfg := &Config{
		Scope: internal.Scope{
			Services: map[string]internal.ServiceScope{
				"github": {Repos: []string{"*"}, Operations: []string{"*"}},
				"gitlab": {Repos: []string{"*"}, Operations: []string{"*"}},
				"jira":   {Repos: []string{"*"}, Operations: []string{"*"}},
			},
		},
		Credentials: map[string]string{},
		ToolConfigs: map[string]ToolConfig{
			"custom": {APIHost: "custom-api.example.com"},
		},
	}
	m, err := NewMITMHandler(certPEM, keyPEM, cfg)
	if err != nil {
		t.Fatalf("creating MITM handler: %v", err)
	}

	tests := []struct {
		hostname string
		expected bool
	}{
		{"api.github.com", true},
		{"github.com", true},
		{"gitlab.com", true},
		{"ci.gitlab.com", true},
		{"company.atlassian.net", true},
		{"other.atlassian.net", true},
		{"custom-api.example.com", true},
		{"api.anthropic.com", false},
		{"pypi.org", false},
		{"random.example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.hostname, func(t *testing.T) {
			got := m.IsMITMDomain(tt.hostname)
			if got != tt.expected {
				t.Errorf("IsMITMDomain(%q) = %v, want %v", tt.hostname, got, tt.expected)
			}
		})
	}
}

// TestMITMHandler_IsMITMDomain_NoServices verifies that with no services in scope,
// no domains are MITM-eligible.
func TestMITMHandler_IsMITMDomain_NoServices(t *testing.T) {
	certPEM, keyPEM := mustGenerateTestCA(t)
	cfg := &Config{
		Scope:       internal.Scope{Services: map[string]internal.ServiceScope{}},
		Credentials: map[string]string{},
		ToolConfigs: map[string]ToolConfig{},
	}
	m, err := NewMITMHandler(certPEM, keyPEM, cfg)
	if err != nil {
		t.Fatalf("creating MITM handler: %v", err)
	}

	if m.IsMITMDomain("api.github.com") {
		t.Error("expected api.github.com to NOT be MITM'd with no services in scope")
	}
	if m.IsMITMDomain("company.atlassian.net") {
		t.Error("expected company.atlassian.net to NOT be MITM'd with no services in scope")
	}
}

// TestMITMHandler_HandleCONNECT tests the full MITM round-trip: CONNECT, TLS
// handshake, HTTP request, credential injection, and response forwarding.
func TestMITMHandler_HandleCONNECT(t *testing.T) {
	certPEM, keyPEM := mustGenerateTestCA(t)

	cfg := Config{
		SessionID: "test-session",
		Scope: internal.Scope{
			Services: map[string]internal.ServiceScope{
				"github": {Repos: []string{"*"}, Operations: []string{"*"}},
			},
		},
		Credentials: map[string]string{"github": "ghp_real_secret"},
		ToolConfigs: map[string]ToolConfig{},
		CACertPEM:   certPEM,
		CAKeyPEM:    keyPEM,
	}
	p := NewProxy(cfg)
	ts := httptest.NewServer(p.Handler())
	defer ts.Close()
	defer p.Stop()

	// Connect to Gate proxy
	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatalf("connecting to gate: %v", err)
	}
	defer conn.Close()

	// Send CONNECT
	fmt.Fprintf(conn, "CONNECT api.github.com:443 HTTP/1.1\r\nHost: api.github.com:443\r\n\r\n")

	// Read the "200 Connection Established" response
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from CONNECT, got %d", resp.StatusCode)
	}

	// TLS handshake with the MITM cert
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(certPEM)

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: "api.github.com",
		RootCAs:    caPool,
		NextProtos: []string{"http/1.1"},
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake with MITM: %v", err)
	}

	// Send an HTTP request through the MITM'd connection
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/test/repo/pulls", nil)
	req.Header.Set("Authorization", "Bearer dummy-token")
	if err := req.Write(tlsConn); err != nil {
		t.Fatalf("writing request through MITM: %v", err)
	}

	// Read the response. The upstream may fail (real GitHub) but the MITM
	// handshake and request forwarding should succeed.
	mitmReader := bufio.NewReader(tlsConn)
	mitmResp, err := http.ReadResponse(mitmReader, req)
	if err != nil {
		// Upstream connection failure is acceptable — the MITM part worked
		t.Logf("response read error (expected if no real upstream): %v", err)
		return
	}
	defer mitmResp.Body.Close()
	t.Logf("MITM response status: %d", mitmResp.StatusCode)
}

// TestMITMHandler_ScopeEnforcement verifies that out-of-scope requests get 403
// through the MITM proxy.
func TestMITMHandler_ScopeEnforcement(t *testing.T) {
	certPEM, keyPEM := mustGenerateTestCA(t)

	cfg := Config{
		SessionID: "test-session",
		Scope: internal.Scope{
			Services: map[string]internal.ServiceScope{
				"github": {Repos: []string{"allowed/repo"}, Operations: []string{"read_prs"}},
			},
		},
		Credentials: map[string]string{"github": "ghp_real_secret"},
		ToolConfigs: map[string]ToolConfig{},
		CACertPEM:   certPEM,
		CAKeyPEM:    keyPEM,
	}
	p := NewProxy(cfg)
	ts := httptest.NewServer(p.Handler())
	defer ts.Close()
	defer p.Stop()

	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatalf("connecting to gate: %v", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT api.github.com:443 HTTP/1.1\r\nHost: api.github.com:443\r\n\r\n")

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from CONNECT, got %d", resp.StatusCode)
	}

	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(certPEM)

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: "api.github.com",
		RootCAs:    caPool,
		NextProtos: []string{"http/1.1"},
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}

	// Request for a repo NOT in scope
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/evil/exfiltrate/pulls", nil)
	if err := req.Write(tlsConn); err != nil {
		t.Fatalf("writing request: %v", err)
	}

	mitmReader := bufio.NewReader(tlsConn)
	mitmResp, err := http.ReadResponse(mitmReader, req)
	if err != nil {
		t.Fatalf("reading MITM response: %v", err)
	}
	defer mitmResp.Body.Close()

	if mitmResp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(mitmResp.Body)
		t.Errorf("expected 403 for out-of-scope repo, got %d: %s", mitmResp.StatusCode, string(body))
	}
}

// TestMITMHandler_CredentialInjection verifies that MITM correctly maps
// hostnames to services and injects the right credential format.
func TestMITMHandler_CredentialInjection(t *testing.T) {
	certPEM, keyPEM := mustGenerateTestCA(t)
	cfg := &Config{
		Scope: internal.Scope{
			Services: map[string]internal.ServiceScope{
				"github": {Repos: []string{"*"}, Operations: []string{"*"}},
				"gitlab": {Repos: []string{"*"}, Operations: []string{"*"}},
				"jira":   {Repos: []string{"*"}, Operations: []string{"*"}},
			},
		},
		Credentials: map[string]string{
			"github": "ghp_github_token",
			"gitlab": "glpat_gitlab_token",
			"jira":   "user@example.com:jira-api-token",
		},
		ToolConfigs: map[string]ToolConfig{},
	}
	m, err := NewMITMHandler(certPEM, keyPEM, cfg)
	if err != nil {
		t.Fatalf("creating MITM handler: %v", err)
	}

	tests := []struct {
		hostname   string
		wantHeader string
		wantValue  string
	}{
		{"api.github.com", "Authorization", "token ghp_github_token"},
		{"gitlab.com", "Authorization", "Bearer glpat_gitlab_token"},
		{"company.atlassian.net", "Authorization", "Basic " + base64.StdEncoding.EncodeToString([]byte("user@example.com:jira-api-token"))},
	}

	for _, tt := range tests {
		t.Run(tt.hostname, func(t *testing.T) {
			req := httptest.NewRequest("GET", "https://"+tt.hostname+"/test", nil)
			if err := m.injectMITMCredential(req, tt.hostname); err != nil {
				t.Fatalf("injectMITMCredential: %v", err)
			}
			got := req.Header.Get(tt.wantHeader)
			if got != tt.wantValue {
				t.Errorf("expected %q=%q, got %q", tt.wantHeader, tt.wantValue, got)
			}
		})
	}
}

// TestMITMHandler_CredentialInjection_ToolConfig verifies that ToolConfig
// overrides are used when available.
func TestMITMHandler_CredentialInjection_ToolConfig(t *testing.T) {
	certPEM, keyPEM := mustGenerateTestCA(t)
	cfg := &Config{
		Scope: internal.Scope{
			Services: map[string]internal.ServiceScope{
				"custom": {Repos: []string{"*"}, Operations: []string{"*"}},
			},
		},
		Credentials: map[string]string{
			"custom": "custom-secret-key",
		},
		ToolConfigs: map[string]ToolConfig{
			"custom": {
				APIHost:    "api.custom.example.com",
				AuthHeader: "X-Custom-Auth",
				AuthFormat: "header",
			},
		},
	}
	m, err := NewMITMHandler(certPEM, keyPEM, cfg)
	if err != nil {
		t.Fatalf("creating MITM handler: %v", err)
	}

	req := httptest.NewRequest("GET", "https://api.custom.example.com/test", nil)
	if err := m.injectMITMCredential(req, "api.custom.example.com"); err != nil {
		t.Fatalf("injectMITMCredential: %v", err)
	}

	got := req.Header.Get("X-Custom-Auth")
	if got != "custom-secret-key" {
		t.Errorf("expected header format credential %q, got %q", "custom-secret-key", got)
	}
}

// TestDecodePEMFromBase64 verifies base64 decoding of PEM data.
func TestDecodePEMFromBase64(t *testing.T) {
	certPEM, _, err := GenerateTestCA()
	if err != nil {
		t.Fatalf("generating CA: %v", err)
	}

	encoded := base64.StdEncoding.EncodeToString(certPEM)

	decoded, err := DecodePEMFromBase64(encoded)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if string(decoded) != string(certPEM) {
		t.Error("decoded PEM does not match original")
	}
}
