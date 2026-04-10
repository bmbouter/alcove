package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestValidateProxyURL(t *testing.T) {
	tests := []struct {
		name      string
		proxyURL  string
		expectErr bool
	}{
		{"valid http proxy", "http://proxy.example.com:8080", false},
		{"valid https proxy", "https://proxy.example.com:8080", false},
		{"valid proxy with auth", "http://user:pass@proxy.example.com:8080", false},
		{"invalid scheme", "ftp://proxy.example.com:8080", true},
		{"missing host", "http://", true},
		{"invalid URL", "not-a-url", true},
		{"no scheme", "proxy.example.com:8080", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProxyURL(tt.proxyURL)
			if (err != nil) != tt.expectErr {
				t.Errorf("validateProxyURL(%q) error = %v, expectErr %v", tt.proxyURL, err, tt.expectErr)
			}
		})
	}
}

func TestParseNoProxy(t *testing.T) {
	tests := []struct {
		name     string
		noProxy  string
		expected []string
	}{
		{"empty string", "", []string{}},
		{"single host", "example.com", []string{"example.com"}},
		{"multiple hosts", "example.com,localhost,192.168.1.1", []string{"example.com", "localhost", "192.168.1.1"}},
		{"with spaces", "example.com, localhost , 192.168.1.1 ", []string{"example.com", "localhost", "192.168.1.1"}},
		{"with empty entries", "example.com,,localhost", []string{"example.com", "localhost"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseNoProxy(tt.noProxy)
			if len(result) != len(tt.expected) {
				t.Errorf("parseNoProxy(%q) length = %d, expected %d", tt.noProxy, len(result), len(tt.expected))
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("parseNoProxy(%q)[%d] = %q, expected %q", tt.noProxy, i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestShouldUseProxy(t *testing.T) {
	tests := []struct {
		name      string
		targetURL string
		noProxy   []string
		expected  bool
	}{
		{"no exclusions", "https://api.example.com", []string{}, true},
		{"exact host match", "https://example.com", []string{"example.com"}, false},
		{"exact host:port match", "https://example.com:8080", []string{"example.com:8080"}, false},
		{"domain suffix match", "https://api.example.com", []string{".example.com"}, false},
		{"wildcard domain match", "https://api.example.com", []string{"*.example.com"}, false},
		{"no match", "https://other.com", []string{"example.com"}, true},
		{"IP match", "https://192.168.1.1", []string{"192.168.1.1"}, false},
		{"CIDR match", "https://192.168.1.1", []string{"192.168.1.0/24"}, false},
		{"port-only match", "https://example.com:8080", []string{"8080"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldUseProxy(tt.targetURL, tt.noProxy)
			if result != tt.expected {
				t.Errorf("shouldUseProxy(%q, %v) = %t, expected %t", tt.targetURL, tt.noProxy, result, tt.expected)
			}
		})
	}
}

func TestResolveProxyConfig(t *testing.T) {
	// Helper function to create a command with proper flags
	createTestCommand := func() *cobra.Command {
		cmd := &cobra.Command{}
		cmd.PersistentFlags().String("proxy-url", "", "")
		cmd.PersistentFlags().String("no-proxy", "", "")
		return cmd
	}

	// Test with flags
	t.Run("flags override environment", func(t *testing.T) {
		// Clean up environment first
		for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			t.Setenv(env, "")
		}

		t.Setenv("HTTP_PROXY", "http://env.proxy:8080")
		t.Setenv("NO_PROXY", "env.example.com")

		cmd := createTestCommand()
		cmd.SetArgs([]string{"--proxy-url", "http://flag.proxy:8080", "--no-proxy", "flag.example.com"})
		cmd.ParseFlags([]string{"--proxy-url", "http://flag.proxy:8080", "--no-proxy", "flag.example.com"})

		config, err := resolveProxyConfig(cmd)
		if err != nil {
			t.Fatalf("resolveProxyConfig() error = %v", err)
		}
		if config.ProxyURL != "http://flag.proxy:8080" {
			t.Errorf("ProxyURL = %q, expected %q", config.ProxyURL, "http://flag.proxy:8080")
		}
		if len(config.NoProxy) != 1 || config.NoProxy[0] != "flag.example.com" {
			t.Errorf("NoProxy = %v, expected [%q]", config.NoProxy, "flag.example.com")
		}
	})

	// Test with environment variables
	t.Run("environment variables", func(t *testing.T) {
		// Clean up environment first
		for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			t.Setenv(env, "")
		}

		t.Setenv("HTTP_PROXY", "http://env.proxy:8080")
		t.Setenv("NO_PROXY", "env.example.com")

		cmd := createTestCommand()

		config, err := resolveProxyConfig(cmd)
		if err != nil {
			t.Fatalf("resolveProxyConfig() error = %v", err)
		}
		if config.ProxyURL != "http://env.proxy:8080" {
			t.Errorf("ProxyURL = %q, expected %q", config.ProxyURL, "http://env.proxy:8080")
		}
		if len(config.NoProxy) != 1 || config.NoProxy[0] != "env.example.com" {
			t.Errorf("NoProxy = %v, expected [%q]", config.NoProxy, "env.example.com")
		}
	})

	// Test HTTPS_PROXY precedence
	t.Run("HTTPS_PROXY precedence over HTTP_PROXY", func(t *testing.T) {
		// Clean up environment first
		for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			t.Setenv(env, "")
		}

		t.Setenv("HTTP_PROXY", "http://http.proxy:8080")
		t.Setenv("HTTPS_PROXY", "https://https.proxy:8080")

		cmd := createTestCommand()

		config, err := resolveProxyConfig(cmd)
		if err != nil {
			t.Fatalf("resolveProxyConfig() error = %v", err)
		}
		if config.ProxyURL != "https://https.proxy:8080" {
			t.Errorf("ProxyURL = %q, expected %q", config.ProxyURL, "https://https.proxy:8080")
		}
	})

	// Test invalid proxy URL from flag
	t.Run("invalid proxy URL from flag", func(t *testing.T) {
		// Clean up environment first
		for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			t.Setenv(env, "")
		}

		cmd := createTestCommand()
		cmd.ParseFlags([]string{"--proxy-url", "invalid-url"})

		_, err := resolveProxyConfig(cmd)
		if err == nil {
			t.Error("Expected error for invalid proxy URL, got nil")
		}
	})

	// Test invalid proxy URL from environment
	t.Run("invalid proxy URL from environment", func(t *testing.T) {
		// Clean up environment first
		for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			t.Setenv(env, "")
		}

		t.Setenv("HTTP_PROXY", "invalid-url")

		cmd := createTestCommand()

		_, err := resolveProxyConfig(cmd)
		if err == nil {
			t.Error("Expected error for invalid proxy URL from environment, got nil")
		}
	})

	// Test no configuration
	t.Run("no proxy configuration", func(t *testing.T) {
		// Clean up environment first
		for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			t.Setenv(env, "")
		}

		cmd := createTestCommand()

		config, err := resolveProxyConfig(cmd)
		if err != nil {
			t.Fatalf("resolveProxyConfig() error = %v", err)
		}
		if config.ProxyURL != "" {
			t.Errorf("ProxyURL = %q, expected empty string", config.ProxyURL)
		}
		if len(config.NoProxy) != 0 {
			t.Errorf("NoProxy = %v, expected empty slice", config.NoProxy)
		}
	})
}