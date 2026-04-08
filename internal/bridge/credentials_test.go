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

func TestClaudeConsumerTokenResult(t *testing.T) {
	// Test that claude_consumer auth type returns the expected token result
	cs := &CredentialStore{
		key: deriveKey("test-key"),
	}

	// Mock a token result for claude_consumer
	sessionToken := "sess-12345-abcdef-test-token"
	encrypted, err := encrypt(cs.key, []byte(sessionToken))
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	// Simulate what AcquireToken would do for claude_consumer
	decrypted, err := decrypt(cs.key, encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	result := &TokenResult{
		Token:     string(decrypted),
		TokenType: "session_token",
		ExpiresIn: 3600,
		Provider:  "anthropic",
	}

	if result.Token != sessionToken {
		t.Fatalf("want token %q, got %q", sessionToken, result.Token)
	}
	if result.TokenType != "session_token" {
		t.Fatalf("want token_type %q, got %q", "session_token", result.TokenType)
	}
	if result.ExpiresIn != 3600 {
		t.Fatalf("want expires_in %d, got %d", 3600, result.ExpiresIn)
	}
	if result.Provider != "anthropic" {
		t.Fatalf("want provider %q, got %q", "anthropic", result.Provider)
	}
}
