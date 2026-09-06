package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aceobservability/ace/backend/pkg/llm"
)

func TestNew_DoesNotDecrypt(t *testing.T) {
	ciphertext := "not-valid-aes-gcm-ciphertext"
	if _, err := decryptToken(ciphertext); err == nil {
		t.Fatal("sanity: blob must fail decryptToken so a factory decrypt would be visible")
	}
	p, err := New(llm.LLMConfig{APIKey: ciphertext})
	if err != nil {
		t.Fatalf("New must not decrypt: %v", err)
	}
	cp, ok := p.(*Provider)
	if !ok {
		t.Fatalf("expected *Provider, got %T", p)
	}
	if cp.EncryptedGHToken != ciphertext {
		t.Errorf("expected ciphertext EncryptedGHToken, got %q", cp.EncryptedGHToken)
	}
}

func TestRegister_ListModelsAndChatViaRegistry(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-for-copilot-tests")
	copilotToken := "tid=registry-session"
	expiresAt := time.Now().Unix() + 3600
	chatJSON := `{"id":"chatcmpl-reg","choices":[{"index":0,"message":{"role":"assistant","content":"from-registry"},"finish_reason":"stop"}]}`

	copilotAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/models":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(copilotModelsPayload()))
		case r.Method == http.MethodPost && r.URL.Path == "/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(chatJSON))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
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

	enc := encryptTestToken(t, "ghp_registry_token")
	clearCopilotTokenCache()

	p, err := llm.New("copilot", llm.LLMConfig{APIKey: enc})
	if err != nil {
		t.Fatalf("llm.New copilot: %v", err)
	}
	cp, ok := p.(*Provider)
	if !ok {
		t.Fatalf("expected *Provider, got %T", p)
	}
	if cp.EncryptedGHToken != enc {
		t.Fatal("EncryptedGHToken must still be the stored ciphertext")
	}
	cp.TokenEndpoint = tokenServer.URL + "/copilot_internal/v2/token"

	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 filtered models, got %d", len(models))
	}

	rr := httptest.NewRecorder()
	if err := p.Chat(context.Background(), llm.ChatRequest{
		Model:    "gpt-4o",
		Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":"hi"}`)},
	}, rr); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(rr.Body.String(), "from-registry") {
		t.Errorf("expected mapped content, got %s", rr.Body.String())
	}
}

func TestListModels_FiltersToChatEnabled(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-for-copilot-tests")
	var tokenCalls int32

	copilotToken := "tid=test-copilot-session-token"
	expiresAt := time.Now().Unix() + 3600

	copilotAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyCopilotHeaders(t, r)
		if r.Header.Get("Authorization") != "Bearer "+copilotToken {
			t.Errorf("expected Authorization 'Bearer %s', got '%s'", copilotToken, r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/models" && r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(copilotModelsPayload()))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer copilotAPI.Close()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		if r.URL.Path != "/copilot_internal/v2/token" {
			t.Errorf("expected path /copilot_internal/v2/token, got %s", r.URL.Path)
		}
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "token ") {
			t.Errorf("expected Authorization to start with 'token ', got '%s'", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      copilotToken,
			"expires_at": expiresAt,
			"endpoints":  map[string]string{"api": copilotAPI.URL},
		})
	}))
	defer tokenServer.Close()

	encToken := encryptTestToken(t, "ghp_test_github_token")
	clearCopilotTokenCache()

	provider := &Provider{
		EncryptedGHToken: encToken,
		TokenEndpoint:    tokenServer.URL + "/copilot_internal/v2/token",
	}

	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 filtered models, got %d: %+v", len(models), models)
	}
	if models[0].ID != "gpt-4o" {
		t.Errorf("expected first model ID 'gpt-4o', got '%s'", models[0].ID)
	}
	if models[0].Name != "GPT-4o" {
		t.Errorf("expected first model Name 'GPT-4o', got '%s'", models[0].Name)
	}
	if models[0].Vendor != "openai" {
		t.Errorf("expected first model Vendor 'openai', got '%s'", models[0].Vendor)
	}
	if models[1].ID != "claude-sonnet-4" {
		t.Errorf("expected second model ID 'claude-sonnet-4', got '%s'", models[1].ID)
	}
	if models[1].Vendor != "anthropic" {
		t.Errorf("expected second model Vendor 'anthropic', got '%s'", models[1].Vendor)
	}
	if calls := atomic.LoadInt32(&tokenCalls); calls != 1 {
		t.Errorf("expected 1 token endpoint call, got %d", calls)
	}
}

func TestListModels_TokenCaching(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-for-copilot-tests")
	var tokenCalls int32

	copilotToken := "tid=cached-session-token"
	expiresAt := time.Now().Unix() + 3600

	copilotAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(copilotModelsPayload()))
	}))
	defer copilotAPI.Close()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      copilotToken,
			"expires_at": expiresAt,
			"endpoints":  map[string]string{"api": copilotAPI.URL},
		})
	}))
	defer tokenServer.Close()

	encToken := encryptTestToken(t, "ghp_caching_test_token")
	clearCopilotTokenCache()

	provider := &Provider{
		EncryptedGHToken: encToken,
		TokenEndpoint:    tokenServer.URL + "/copilot_internal/v2/token",
	}

	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("first ListModels call returned error: %v", err)
	}
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("second ListModels call returned error: %v", err)
	}
	if calls := atomic.LoadInt32(&tokenCalls); calls != 1 {
		t.Errorf("expected token endpoint to be called 1 time (caching), got %d", calls)
	}
}

func TestListModels_TokenExpired(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-for-copilot-tests")
	var tokenCalls int32

	copilotAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(copilotModelsPayload()))
	}))
	defer copilotAPI.Close()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "tid=fresh-token",
			"expires_at": time.Now().Unix() + 3600,
			"endpoints":  map[string]string{"api": copilotAPI.URL},
		})
	}))
	defer tokenServer.Close()

	encToken := encryptTestToken(t, "ghp_expired_test_token")
	clearCopilotTokenCache()
	copilotTokenCache.Store(hashToken("ghp_expired_test_token"), cachedCopilotToken{
		token:       "tid=expired",
		apiEndpoint: copilotAPI.URL,
		expiresAt:   time.Now().Unix() - 10,
	})

	provider := &Provider{
		EncryptedGHToken: encToken,
		TokenEndpoint:    tokenServer.URL + "/copilot_internal/v2/token",
	}

	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	if calls := atomic.LoadInt32(&tokenCalls); calls != 1 {
		t.Errorf("expected 1 token fetch for expired cache, got %d", calls)
	}
}

func TestListModels_TokenEndpointError(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-for-copilot-tests")
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer tokenServer.Close()

	encToken := encryptTestToken(t, "ghp_bad_creds_token")
	clearCopilotTokenCache()

	provider := &Provider{
		EncryptedGHToken: encToken,
		TokenEndpoint:    tokenServer.URL + "/copilot_internal/v2/token",
	}

	_, err := provider.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error when token endpoint returns 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected error to contain '401', got: %s", err.Error())
	}
}

func TestListModels_CopilotHeadersOnTokenEndpoint(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-for-copilot-tests")
	copilotAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(copilotModelsPayload()))
	}))
	defer copilotAPI.Close()

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Editor-Version"); got != "vscode/1.100.0" {
			t.Errorf("expected Editor-Version on token endpoint, got '%s'", got)
		}
		if got := r.Header.Get("Editor-Plugin-Version"); got != "copilot/1.300.0" {
			t.Errorf("expected Editor-Plugin-Version on token endpoint, got '%s'", got)
		}
		if got := r.Header.Get("User-Agent"); got != "GithubCopilot/1.300.0" {
			t.Errorf("expected User-Agent on token endpoint, got '%s'", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "tid=header-test",
			"expires_at": time.Now().Unix() + 3600,
			"endpoints":  map[string]string{"api": copilotAPI.URL},
		})
	}))
	defer tokenServer.Close()

	encToken := encryptTestToken(t, "ghp_header_check_token")
	clearCopilotTokenCache()

	provider := &Provider{
		EncryptedGHToken: encToken,
		TokenEndpoint:    tokenServer.URL + "/copilot_internal/v2/token",
	}

	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
}
