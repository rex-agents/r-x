// Package harness is rux's own agent harness: a model-agnostic agent loop with coding
// tools that run on this machine or in a rox guest. Each agent talks to a model through
// a Provider: Claude through the Anthropic API, or anything that speaks the OpenAI chat
// completions API (OpenAI, OpenRouter, Ollama, LM Studio, vLLM, Groq, ...).
package harness

import (
	"context"
	"encoding/json"
)

// ToolSpec describes a tool to the model. Schema is a JSON Schema object.
type ToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
}

// ToolCall is one tool call the model asked for.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult answers one ToolCall.
type ToolResult struct {
	ID      string
	Content string
	IsError bool
}

// Delta is streamed output: Kind is "text" or "thinking".
type Delta struct {
	Kind string
	Text string
}

// Usage counts one step's tokens.
type Usage struct {
	Input, Output, CacheRead int64
}

// Stop reasons a Turn can end with.
const (
	StopEnd       = "end"
	StopTools     = "tools"
	StopMaxTokens = "max_tokens"
	StopRefusal   = "refusal"
	StopOther     = "other"
)

// Turn is one model response.
type Turn struct {
	Text  string
	Calls []ToolCall
	Stop  string
	// Detail explains a refusal or an unusual stop.
	Detail string
	Usage  Usage
}

// Conversation is one agent's history with a model. A provider keeps its own native
// history, so nothing it needs to replay (thinking blocks, compaction) is lost.
type Conversation interface {
	// User adds a user message.
	User(text string)
	// Results answers the last turn's tool calls, with an optional note from the user.
	Results(results []ToolResult, note string)
	// Step asks the model for its next turn, streaming output to onDelta.
	Step(ctx context.Context, tools []ToolSpec, onDelta func(Delta)) (Turn, error)
}

// Provider starts conversations with one model.
type Provider interface {
	// Name is "provider/model".
	Name() string
	New(system string) Conversation
}
