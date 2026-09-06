package copilot

import "github.com/aceobservability/ace/backend/pkg/llm"

func init() {
	llm.RegisterLLM("copilot", New)
}
