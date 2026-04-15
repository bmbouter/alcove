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

package bridge

import "testing"

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := make([]byte, 32)
	plaintext := []byte("sk-ant-api03-secret-key")
	encrypted, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if string(encrypted) == string(plaintext) {
		t.Fatal("encrypted should differ")
	}
	decrypted, err := decrypt(key, encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("got %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptDecryptWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	key2[0] = 1
	encrypted, _ := encrypt(key1, []byte("secret"))
	_, err := decrypt(key2, encrypted)
	if err == nil {
		t.Fatal("wrong key should fail")
	}
}

func TestEncryptDecryptOAuthTokenRoundtrip(t *testing.T) {
	key := make([]byte, 32)
	plaintext := []byte("sk-ant-oat01-oauth-token-value")
	encrypted, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if string(encrypted) == string(plaintext) {
		t.Fatal("encrypted should differ")
	}
	decrypted, err := decrypt(key, encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("got %q, want %q", decrypted, plaintext)
	}
}

func TestProviderCategory(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"anthropic", "llm"},
		{"google-vertex", "llm"},
		{"claude-oauth", "llm"},
		{"github", "scm"},
		{"gitlab", "scm"},
		{"jira", "scm"},
		{"splunk", "scm"},
		{"generic", "generic"},
		{"unknown-thing", "generic"},
		{"", "generic"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := ProviderCategory(tt.provider)
			if got != tt.want {
				t.Errorf("ProviderCategory(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

func TestDeriveKey(t *testing.T) {
	key := deriveKey("my-master-password")
	if len(key) != 32 {
		t.Fatalf("want 32 bytes, got %d", len(key))
	}
	key2 := deriveKey("my-master-password")
	if string(key) != string(key2) {
		t.Fatal("same input should give same key")
	}
	key3 := deriveKey("different-password")
	if string(key) == string(key3) {
		t.Fatal("different input should give different key")
	}
}
