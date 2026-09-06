# ace-llm-copilot

Compile-time GitHub Copilot adapter for [Ace](https://github.com/aceobservability/ace).

Implements `github.com/aceobservability/ace/backend/pkg/llm` (`AIProvider`). `init` calls `llm.RegisterLLM("copilot", New)`. Ace blank-imports this module.

```go
import _ "github.com/aceobservability/ace-llm-copilot"
```

`LLMConfig.APIKey` is Ace's AES-GCM ciphertext (same `JWT_SECRET` derivation as `backend/internal/crypto`). The factory stores it. `ListModels` and `Chat` decrypt on use.

Token exchange hits GitHub's Copilot token endpoint, then `/models` and `/chat/completions` on `endpoints.api`. Tests use `httptest` fixtures. No live Copilot API in CI.
