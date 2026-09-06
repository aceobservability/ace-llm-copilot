package copilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aceobservability/ace/backend/pkg/llm"
)

const defaultCopilotTokenEndpoint = "https://api.github.com/copilot_internal/v2/token"

// Outbound HTTP uses Go's default http.Client (timeout only). endpoints.api
// from GitHub's token JSON is used as-is (not Ace validateBaseURL). See
// docs/adr/0003-outbound-http-ssrf-policy-seams.md.
type Provider struct {
	EncryptedGHToken string
	TokenEndpoint    string
}

func New(cfg llm.LLMConfig) (llm.AIProvider, error) {
	return &Provider{EncryptedGHToken: cfg.APIKey}, nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

var copilotTokenCache sync.Map

type cachedCopilotToken struct {
	token       string
	apiEndpoint string
	expiresAt   int64
}

func setCopilotHeaders(req *http.Request, sessionToken string) {
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	req.Header.Set("Editor-Version", "vscode/1.100.0")
	req.Header.Set("Editor-Plugin-Version", "copilot/1.300.0")
	req.Header.Set("User-Agent", "GithubCopilot/1.300.0")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
}

func (p *Provider) getTokenEndpoint() string {
	if p.TokenEndpoint != "" {
		return p.TokenEndpoint
	}
	return defaultCopilotTokenEndpoint
}

func (p *Provider) getCopilotSessionToken(ctx context.Context, ghToken string) (sessionToken string, apiEndpoint string, err error) {
	cacheKey := hashToken(ghToken)

	if cached, ok := copilotTokenCache.Load(cacheKey); ok {
		entry := cached.(cachedCopilotToken)
		if entry.expiresAt-60 > time.Now().Unix() {
			return entry.token, entry.apiEndpoint, nil
		}
		copilotTokenCache.Delete(cacheKey)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", p.getTokenEndpoint(), nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Authorization", "token "+ghToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Editor-Version", "vscode/1.100.0")
	req.Header.Set("Editor-Plugin-Version", "copilot/1.300.0")
	req.Header.Set("User-Agent", "GithubCopilot/1.300.0")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to reach GitHub token endpoint: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("copilot token request failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var tokenResp struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return "", "", fmt.Errorf("failed to decode Copilot token response: %w", err)
	}

	if tokenResp.Token == "" {
		return "", "", fmt.Errorf("empty Copilot token returned")
	}

	apiURL := tokenResp.Endpoints.API
	if apiURL == "" {
		apiURL = "https://api.individual.githubcopilot.com"
	}

	copilotTokenCache.Store(cacheKey, cachedCopilotToken{
		token:       tokenResp.Token,
		apiEndpoint: apiURL,
		expiresAt:   tokenResp.ExpiresAt,
	})

	return tokenResp.Token, apiURL, nil
}

type copilotModelsResponse struct {
	Data []struct {
		ID                  string `json:"id"`
		Name                string `json:"name"`
		Vendor              string `json:"vendor"`
		ModelPickerEnabled  bool   `json:"model_picker_enabled"`
		ModelPickerCategory string `json:"model_picker_category"`
		Preview             bool   `json:"preview"`
		Policy              struct {
			State string `json:"state"`
		} `json:"policy"`
		SupportedEndpoints []string `json:"supported_endpoints"`
	} `json:"data"`
}

func (p *Provider) ListModels(ctx context.Context) ([]llm.AIModel, error) {
	ghToken, err := decryptToken(p.EncryptedGHToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt GitHub token: %w", err)
	}

	sessionToken, apiEndpoint, err := p.getCopilotSessionToken(ctx, ghToken)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiEndpoint+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create models request: %w", err)
	}
	setCopilotHeaders(req, sessionToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach Copilot API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("copilot models API returned %d: %s", resp.StatusCode, string(body))
	}

	var raw copilotModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode Copilot models response: %w", err)
	}

	models := make([]llm.AIModel, 0)
	for _, m := range raw.Data {
		if !m.ModelPickerEnabled || m.Policy.State != "enabled" {
			continue
		}
		supportsChat := false
		for _, ep := range m.SupportedEndpoints {
			if ep == "/chat/completions" {
				supportsChat = true
				break
			}
		}
		if !supportsChat {
			continue
		}

		models = append(models, llm.AIModel{
			ID:       m.ID,
			Name:     m.Name,
			Vendor:   m.Vendor,
			Category: m.ModelPickerCategory,
		})
	}

	return models, nil
}

func (p *Provider) Chat(ctx context.Context, chatReq llm.ChatRequest, w http.ResponseWriter) error {
	ghToken, err := decryptToken(p.EncryptedGHToken)
	if err != nil {
		return fmt.Errorf("failed to decrypt GitHub token: %w", err)
	}

	sessionToken, apiEndpoint, err := p.getCopilotSessionToken(ctx, ghToken)
	if err != nil {
		return err
	}

	body := map[string]interface{}{
		"model":    chatReq.Model,
		"messages": chatReq.Messages,
		"stream":   chatReq.Stream,
	}
	if len(chatReq.Tools) > 0 {
		body["tools"] = chatReq.Tools
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiEndpoint+"/chat/completions", strings.NewReader(string(bodyJSON)))
	if err != nil {
		return fmt.Errorf("failed to create chat request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	setCopilotHeaders(req, sessionToken)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to reach Copilot API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("copilot API returned %d: %s", resp.StatusCode, string(respBody))
	}

	if !chatReq.Stream {
		w.Header().Set("Content-Type", "application/json")
		io.Copy(w, resp.Body)
		return nil
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}

	return nil
}
