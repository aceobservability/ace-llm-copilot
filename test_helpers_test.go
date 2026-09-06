package copilot

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"testing"
)

func encryptTestToken(t *testing.T, plaintext string) string {
	t.Helper()
	key, err := deriveEncryptionKey()
	if err != nil {
		t.Fatalf("failed to derive encryption key: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes cipher: %v", err)
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("nonce: %v", err)
	}
	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext)
}

func copilotModelsPayload() string {
	return `{
		"data": [
			{
				"id": "gpt-4o",
				"name": "GPT-4o",
				"vendor": "openai",
				"model_picker_enabled": true,
				"model_picker_category": "chat",
				"preview": false,
				"policy": {"state": "enabled"},
				"supported_endpoints": ["/chat/completions"]
			},
			{
				"id": "claude-sonnet-4",
				"name": "Claude Sonnet 4",
				"vendor": "anthropic",
				"model_picker_enabled": true,
				"model_picker_category": "chat",
				"preview": false,
				"policy": {"state": "enabled"},
				"supported_endpoints": ["/chat/completions"]
			},
			{
				"id": "disabled-model",
				"name": "Disabled Model",
				"vendor": "test",
				"model_picker_enabled": false,
				"model_picker_category": "chat",
				"preview": false,
				"policy": {"state": "enabled"},
				"supported_endpoints": ["/chat/completions"]
			},
			{
				"id": "policy-blocked",
				"name": "Policy Blocked",
				"vendor": "test",
				"model_picker_enabled": true,
				"model_picker_category": "chat",
				"preview": false,
				"policy": {"state": "disabled"},
				"supported_endpoints": ["/chat/completions"]
			},
			{
				"id": "embeddings-only",
				"name": "Embeddings Only",
				"vendor": "test",
				"model_picker_enabled": true,
				"model_picker_category": "embeddings",
				"preview": false,
				"policy": {"state": "enabled"},
				"supported_endpoints": ["/embeddings"]
			}
		]
	}`
}

var copilotExpectedHeaders = map[string]string{
	"Editor-Version":         "vscode/1.100.0",
	"Editor-Plugin-Version":  "copilot/1.300.0",
	"User-Agent":             "GithubCopilot/1.300.0",
	"Copilot-Integration-Id": "vscode-chat",
}

func verifyCopilotHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	for k, v := range copilotExpectedHeaders {
		if got := r.Header.Get(k); got != v {
			t.Errorf("expected header %s=%q, got %q", k, v, got)
		}
	}
}

func clearCopilotTokenCache() {
	copilotTokenCache.Range(func(key, value any) bool {
		copilotTokenCache.Delete(key)
		return true
	})
}
