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
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bmbouter/alcove/internal"
)

const certCacheMaxSize = 256

// CertCache is an LRU cache for per-hostname leaf certificates.
type CertCache struct {
	mu      sync.Mutex
	entries map[string]*certCacheEntry
	order   []string // oldest first for LRU eviction
}

type certCacheEntry struct {
	cert *tls.Certificate
}

func newCertCache() *CertCache {
	return &CertCache{
		entries: make(map[string]*certCacheEntry),
	}
}

// GetOrCreate returns a cached leaf cert for the hostname, or generates a new
// one signed by the given CA.
func (cc *CertCache) GetOrCreate(hostname string, caCert *x509.Certificate, caKey crypto.PrivateKey) (*tls.Certificate, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if e, ok := cc.entries[hostname]; ok {
		// Move to end (most recently used)
		cc.moveToEnd(hostname)
		return e.cert, nil
	}

	cert, err := generateLeafCert(hostname, caCert, caKey)
	if err != nil {
		return nil, err
	}

	// Evict oldest if at capacity
	if len(cc.entries) >= certCacheMaxSize {
		oldest := cc.order[0]
		cc.order = cc.order[1:]
		delete(cc.entries, oldest)
	}

	cc.entries[hostname] = &certCacheEntry{cert: cert}
	cc.order = append(cc.order, hostname)
	return cert, nil
}

func (cc *CertCache) moveToEnd(hostname string) {
	for i, h := range cc.order {
		if h == hostname {
			cc.order = append(cc.order[:i], cc.order[i+1:]...)
			cc.order = append(cc.order, hostname)
			return
		}
	}
}

func (cc *CertCache) len() int {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return len(cc.entries)
}

func generateLeafCert(hostname string, caCert *x509.Certificate, caKey crypto.PrivateKey) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating leaf key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: hostname,
		},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(1 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{hostname},
	}

	if ip := net.ParseIP(hostname); ip != nil {
		template.IPAddresses = []net.IP{ip}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("creating leaf certificate: %w", err)
	}

	leafCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parsing leaf certificate: %w", err)
	}

	return &tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
		Leaf:        leafCert,
	}, nil
}

// MITMHandler handles CONNECT requests by performing TLS interception,
// credential injection, and scope enforcement.
type MITMHandler struct {
	caCert          *x509.Certificate
	caKey           crypto.PrivateKey
	certCache       *CertCache
	config          *Config
	domains         map[string]bool          // set of MITM-eligible hostnames (exact)
	policyRuleHosts []internal.PolicyRule     // for wildcard host matching
}

// NewMITMHandler parses the PEM-encoded CA cert+key and initializes the handler.
func NewMITMHandler(caCertPEM, caKeyPEM []byte, config *Config) (*MITMHandler, error) {
	certBlock, _ := pem.Decode(caCertPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("failed to decode CA certificate PEM")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing CA certificate: %w", err)
	}

	keyBlock, _ := pem.Decode(caKeyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("failed to decode CA key PEM")
	}
	caKey, err := parsePrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing CA key: %w", err)
	}

	m := &MITMHandler{
		caCert:    caCert,
		caKey:     caKey,
		certCache: newCertCache(),
		config:    config,
		domains:   make(map[string]bool),
	}

	// Build domain set from config.Scope.Services
	for service := range config.Scope.Services {
		switch service {
		case "github":
			m.domains["api.github.com"] = true
			m.domains["github.com"] = true
		case "gitlab":
			host := config.GitLabHost
			if host == "" {
				host = "gitlab.com"
			}
			m.domains[host] = true
			// Mark gitlab for wildcard matching (handled in IsMITMDomain)
		case "jira":
			// Handled by wildcard matching in IsMITMDomain
		}
	}

	// Add custom hosts from ToolConfigs
	for _, tc := range config.ToolConfigs {
		if tc.APIHost != "" {
			m.domains[tc.APIHost] = true
		}
	}

	// Add hosts from PolicyRules (store wildcard patterns separately)
	for _, rule := range config.PolicyRules {
		if rule.Allow.Host != "" {
			m.domains[rule.Allow.Host] = true
		}
	}
	m.policyRuleHosts = config.PolicyRules

	// Log all MITM domains for debugging
	domainList := make([]string, 0, len(m.domains))
	for d := range m.domains {
		domainList = append(domainList, d)
	}
	log.Printf("gate: MITM domains: %v (services: %v, policyRules: %d)", domainList, func() []string {
		s := make([]string, 0)
		for k := range config.Scope.Services {
			s = append(s, k)
		}
		return s
	}(), len(config.PolicyRules))

	return m, nil
}

func parsePrivateKey(der []byte) (crypto.PrivateKey, error) {
	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(der); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("failed to parse private key")
}

// IsMITMDomain checks if a hostname should be MITM'd.
func (m *MITMHandler) IsMITMDomain(hostname string) bool {
	if m.domains[hostname] {
		return true
	}

	// Wildcard matching for gitlab subdomains
	if _, ok := m.config.Scope.Services["gitlab"]; ok {
		if hostname == "gitlab.com" || strings.HasSuffix(hostname, ".gitlab.com") {
			return true
		}
	}

	// Wildcard matching for Jira/Atlassian
	if _, ok := m.config.Scope.Services["jira"]; ok {
		if strings.HasSuffix(hostname, ".atlassian.net") {
			return true
		}
	}

	// Check policy rule hosts with wildcard matching
	for _, rule := range m.policyRuleHosts {
		if matchHost(hostname, rule.Allow.Host) {
			return true
		}
	}

	return false
}

// HandleCONNECT performs MITM TLS interception on a CONNECT tunnel.
func (m *MITMHandler) HandleCONNECT(w http.ResponseWriter, r *http.Request, targetHost string) {
	hostname := hostOnly(targetHost)
	log.Printf("gate: MITM CONNECT start: target=%s hostname=%s", targetHost, hostname)

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		log.Printf("gate: MITM CONNECT %s: hijacking not supported", hostname)
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		log.Printf("gate: MITM hijack failed for %s: %v", hostname, err)
		return
	}
	defer clientConn.Close()

	log.Printf("gate: MITM %s: sending 200 Connection Established", hostname)
	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	leafCert, err := m.certCache.GetOrCreate(hostname, m.caCert, m.caKey)
	if err != nil {
		log.Printf("gate: MITM cert generation failed for %s: %v", hostname, err)
		return
	}
	log.Printf("gate: MITM %s: leaf cert ready, starting TLS handshake", hostname)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*leafCert},
		NextProtos:   []string{"http/1.1"},
	}
	tlsConn := tls.Server(clientConn, tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		log.Printf("gate: MITM TLS handshake FAILED for %s: %v", hostname, err)
		return
	}
	defer tlsConn.Close()
	log.Printf("gate: MITM %s: TLS handshake complete, negotiated=%s", hostname, tlsConn.ConnectionState().NegotiatedProtocol)

	reader := bufio.NewReader(tlsConn)
	reqCount := 0
	for {
		log.Printf("gate: MITM %s: waiting for HTTP request (req #%d)...", hostname, reqCount+1)
		req, err := http.ReadRequest(reader)
		if err != nil {
			if err != io.EOF {
				log.Printf("gate: MITM read request failed for %s (req #%d): %v", hostname, reqCount+1, err)
			} else {
				log.Printf("gate: MITM %s: client EOF after %d requests", hostname, reqCount)
			}
			return
		}
		reqCount++

		req.URL.Scheme = "https"
		req.URL.Host = hostname
		log.Printf("gate: MITM %s: received %s %s (req #%d)", hostname, req.Method, req.URL.String(), reqCount)

		m.handleMITMRequest(tlsConn, req, hostname, targetHost)
		log.Printf("gate: MITM %s: completed %s %s (req #%d)", hostname, req.Method, req.URL.String(), reqCount)
	}
}

func (m *MITMHandler) handleMITMRequest(clientConn net.Conn, req *http.Request, hostname, targetHost string) {
	// Check scope
	var result AccessResult
	if len(m.config.PolicyRules) > 0 {
		result = CheckPolicyRules(req.Method, req.URL.String(), m.config.PolicyRules)
		log.Printf("gate: MITM %s: policy check result: allowed=%v reason=%q", hostname, result.Allowed, result.Reason)
	} else {
		result = CheckAccess(req.Method, req.URL.String(), m.config.Scope)
		log.Printf("gate: MITM %s: scope check result: allowed=%v reason=%q", hostname, result.Allowed, result.Reason)
	}
	if !result.Allowed {
		if m.config.EnforcementMode == "monitor" {
			log.Printf("gate: MITM monitor: would deny %s %s: %s (allowing)", req.Method, req.URL.String(), result.Reason)
		} else {
			resp := &http.Response{
				StatusCode: http.StatusForbidden,
				ProtoMajor: 1,
				ProtoMinor: 1,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("Forbidden: " + result.Reason)),
			}
			resp.Header.Set("Content-Type", "text/plain")
			_ = resp.Write(clientConn)
			log.Printf("gate: MITM denied %s %s: %s", req.Method, req.URL.String(), result.Reason)
			return
		}
	}

	// Inject credentials
	log.Printf("gate: MITM %s: injecting credentials...", hostname)
	if err := m.injectMITMCredential(req, hostname); err != nil {
		log.Printf("gate: MITM credential injection failed for %s: %v", hostname, err)
	}
	authHeader := req.Header.Get("Authorization")
	if authHeader != "" {
		// Mask the credential but show the type
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 {
			log.Printf("gate: MITM %s: auth header set: type=%s len=%d", hostname, parts[0], len(parts[1]))
		} else {
			log.Printf("gate: MITM %s: auth header set: raw len=%d", hostname, len(authHeader))
		}
	} else {
		log.Printf("gate: MITM %s: WARNING no auth header after credential injection", hostname)
	}

	// Forward to real upstream over TLS
	log.Printf("gate: MITM %s: dialing upstream %s...", hostname, ensurePort(targetHost, "443"))
	upstreamConn, err := tls.Dial("tcp", ensurePort(targetHost, "443"), &tls.Config{
		ServerName: hostname,
	})
	if err != nil {
		log.Printf("gate: MITM upstream dial FAILED for %s: %v", targetHost, err)
		resp := &http.Response{
			StatusCode: http.StatusBadGateway,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("Bad Gateway")),
		}
		_ = resp.Write(clientConn)
		return
	}
	defer upstreamConn.Close()
	log.Printf("gate: MITM %s: upstream connected", hostname)

	// Send the request to upstream
	req.RequestURI = ""
	upstreamURL := *req.URL
	req.URL = &upstreamURL

	log.Printf("gate: MITM %s: writing request to upstream...", hostname)
	if err := req.Write(upstreamConn); err != nil {
		log.Printf("gate: MITM upstream write FAILED for %s: %v", targetHost, err)
		return
	}
	log.Printf("gate: MITM %s: request written, reading response...", hostname)

	// Read the response from upstream
	upstreamReader := bufio.NewReader(upstreamConn)
	resp, err := http.ReadResponse(upstreamReader, req)
	if err != nil {
		log.Printf("gate: MITM upstream response read FAILED for %s: %v", targetHost, err)
		return
	}
	defer resp.Body.Close()
	log.Printf("gate: MITM %s: upstream response: %d %s (content-length=%d)", hostname, resp.StatusCode, resp.Status, resp.ContentLength)

	// Forward response to client
	log.Printf("gate: MITM %s: forwarding response to client...", hostname)
	if err := resp.Write(clientConn); err != nil {
		log.Printf("gate: MITM response write FAILED for %s: %v", targetHost, err)
		return
	}
	log.Printf("gate: MITM %s: response forwarded successfully", hostname)
}

// injectMITMCredential injects real credentials into the request based on the
// hostname and service configuration.
func (m *MITMHandler) injectMITMCredential(req *http.Request, hostname string) error {
	service := m.identifyServiceFromHost(hostname)
	if service == "" {
		log.Printf("gate: MITM credential: no service identified for host %s", hostname)
		return nil
	}
	log.Printf("gate: MITM credential: host=%s service=%s", hostname, service)

	// Check ToolConfigs first for custom credential injection
	if tc, ok := m.config.ToolConfigs[service]; ok {
		cred, ok := m.config.Credentials[service]
		if !ok || cred == "" {
			return nil
		}
		switch tc.AuthFormat {
		case "bearer":
			req.Header.Set(tc.AuthHeader, "Bearer "+cred)
		case "header":
			req.Header.Set(tc.AuthHeader, cred)
		case "basic":
			req.Header.Set(tc.AuthHeader, "Basic "+base64.StdEncoding.EncodeToString([]byte(cred)))
		default:
			req.Header.Set(tc.AuthHeader, "Bearer "+cred)
		}
		return nil
	}

	cred, ok := m.config.Credentials[service]
	if !ok || cred == "" {
		return nil
	}

	switch service {
	case "github":
		req.Header.Set("Authorization", "token "+cred)
	case "gitlab":
		req.Header.Set("Authorization", "Bearer "+cred)
	case "jira":
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cred)))
	}

	return nil
}

func (m *MITMHandler) identifyServiceFromHost(hostname string) string {
	// Check ToolConfigs for matching API host
	for name, tc := range m.config.ToolConfigs {
		if tc.APIHost == hostname {
			return name
		}
	}

	switch {
	case hostname == "api.github.com" || hostname == "github.com" || strings.HasSuffix(hostname, ".github.com"):
		return "github"
	case hostname == "gitlab.com" || strings.HasSuffix(hostname, ".gitlab.com"):
		return "gitlab"
	case strings.HasSuffix(hostname, ".atlassian.net"):
		return "jira"
	default:
		return ""
	}
}

// DecodePEMFromBase64 decodes base64-encoded PEM data, as used by the
// GATE_CA_CERT_PEM and GATE_CA_KEY_PEM environment variables.
func DecodePEMFromBase64(encoded string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	return data, nil
}

// GenerateTestCA creates a self-signed CA certificate and key for testing.
func GenerateTestCA() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: "Alcove Gate Test CA",
		},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}
