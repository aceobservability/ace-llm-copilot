package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aceobservability/ace/backend/pkg/llm"
)

func TestChat_NonStreaming(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-for-copilot-tests")
	expectedResponse := `{"id":"chatcmpl-456","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hello from Copilot!"},"finish_reason":"stop"}]}`

	copilotToken := "tid=chat-test-token"
	expiresAt := time.Now().Unix() + 3600

	copilotAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyCopilotHeaders(t, r)
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected path /chat/completions, got %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer "+copilotToken {
			t.Errorf("expected Authorization 'Bearer %s', got '%s'", copilotToken, r.Header.Get("Authorization"))
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if body["model"] != "gpt-4o" {
			t.Errorf("expected model 'gpt-4o', got '%v'", body["model"])
		}
		if body["stream"] != false {
			t.Errorf("expected stream false, got %v", body["stream"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(expectedResponse))
	}))
	defer copilotAPI.Close()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      copilotToken,
			"expires_at": expiresAt,
			"endpoints":  map[string]string{"api": copilotAPI.URL},
		})
	}))
	defer tokenServer.Close()

	encToken := encryptTestToken(t, "ghp_chat_nonstream_token")
	clearCopilotTokenCache()

	provider := &Provider{
		EncryptedGHToken: encToken,
		TokenEndpoint:    tokenServer.URL + "/copilot_internal/v2/token",
	}

	chatReq := llm.ChatRequest{
		Model:    "gpt-4o",
		Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":"Hi"}`)},
		Stream:   false,
	}

	recorder := httptest.NewRecorder()
	if err := provider.Chat(context.Background(), chatReq, recorder); err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if ct := recorder.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got '%s'", ct)
	}
	if respBody := recorder.Body.String(); respBody != expectedResponse {
		t.Errorf("expected response body %q, got %q", expectedResponse, respBody)
	}
}

func TestChat_Streaming(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-for-copilot-tests")
	chunk1 := `data: {"id":"chatcmpl-789","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}`
	chunk2 := `data: {"id":"chatcmpl-789","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"lo!"},"finish_reason":null}]}`
	chunk3 := `data: {"id":"chatcmpl-789","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	chunkDone := `data: [DONE]`
	sseBody := fmt.Sprintf("%s\n\n%s\n\n%s\n\n%s\n\n", chunk1, chunk2, chunk3, chunkDone)

	copilotToken := "tid=stream-test-token"
	expiresAt := time.Now().Unix() + 3600

	copilotAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyCopilotHeaders(t, r)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if body["stream"] != true {
			t.Errorf("expected stream true, got %v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sseBody))
	}))
	defer copilotAPI.Close()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      copilotToken,
			"expires_at": expiresAt,
			"endpoints":  map[string]string{"api": copilotAPI.URL},
		})
	}))
	defer tokenServer.Close()

	encToken := encryptTestToken(t, "ghp_chat_stream_token")
	clearCopilotTokenCache()

	provider := &Provider{
		EncryptedGHToken: encToken,
		TokenEndpoint:    tokenServer.URL + "/copilot_internal/v2/token",
	}

	chatReq := llm.ChatRequest{
		Model:    "claude-sonnet-4",
		Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":"Hi"}`)},
		Stream:   true,
	}

	recorder := httptest.NewRecorder()
	if err := provider.Chat(context.Background(), chatReq, recorder); err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if ct := recorder.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type 'text/event-stream', got '%s'", ct)
	}
	if cc := recorder.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("expected Cache-Control 'no-cache', got '%s'", cc)
	}
	respBody := recorder.Body.String()
	if !strings.Contains(respBody, "data: {") {
		t.Errorf("expected SSE data in response, got: %s", respBody)
	}
	if !strings.Contains(respBody, "[DONE]") {
		t.Errorf("expected [DONE] in response, got: %s", respBody)
	}
	if !strings.Contains(respBody, "Hel") {
		t.Errorf("expected 'Hel' chunk content in response, got: %s", respBody)
	}
	if !strings.Contains(respBody, "lo!") {
		t.Errorf("expected 'lo!' chunk content in response, got: %s", respBody)
	}
}

func TestChat_UpstreamError(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-for-copilot-tests")
	copilotToken := "tid=error-test-token"
	expiresAt := time.Now().Unix() + 3600

	copilotAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer copilotAPI.Close()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      copilotToken,
			"expires_at": expiresAt,
			"endpoints":  map[string]string{"api": copilotAPI.URL},
		})
	}))
	defer tokenServer.Close()

	encToken := encryptTestToken(t, "ghp_error_test_token")
	clearCopilotTokenCache()

	provider := &Provider{
		EncryptedGHToken: encToken,
		TokenEndpoint:    tokenServer.URL + "/copilot_internal/v2/token",
	}

	chatReq := llm.ChatRequest{
		Model:    "gpt-4o",
		Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":"Hi"}`)},
		Stream:   false,
	}

	recorder := httptest.NewRecorder()
	err := provider.Chat(context.Background(), chatReq, recorder)
	if err == nil {
		t.Fatal("expected error on upstream 429, got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("expected error to contain '429', got: %s", err.Error())
	}
}
