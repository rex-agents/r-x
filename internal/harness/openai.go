package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// OpenAI talks to any server that speaks the OpenAI chat completions API: OpenAI itself,
// OpenRouter, Ollama, LM Studio, vLLM, Groq, Together, DeepSeek and the like.
type OpenAI struct {
	// Label is the provider's name in "label/model".
	Label   string
	Model   string
	BaseURL string
	APIKey  string
	// Headers are extra request headers.
	Headers map[string]string
}

func (p *OpenAI) Name() string { return firstNonEmpty(p.Label, "openai") + "/" + p.Model }

func (p *OpenAI) New(system string) Conversation {
	c := &openaiConv{p: p}
	if system != "" {
		c.messages = append(c.messages, oaMessage{Role: "system", Content: system})
	}
	return c
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openaiConv struct {
	p        *OpenAI
	messages []oaMessage
}

func (c *openaiConv) User(text string) {
	if n := len(c.messages); n > 0 && c.messages[n-1].Role == "user" {
		c.messages[n-1].Content += "\n\n" + text
		return
	}
	c.messages = append(c.messages, oaMessage{Role: "user", Content: text})
}

func (c *openaiConv) Results(results []ToolResult, note string) {
	for _, r := range results {
		content := r.Content
		if r.IsError {
			content = "Error: " + content
		}
		c.messages = append(c.messages, oaMessage{Role: "tool", ToolCallID: r.ID, Content: content})
	}
	if note != "" {
		c.messages = append(c.messages, oaMessage{Role: "user", Content: note})
	}
}

func (c *openaiConv) Step(ctx context.Context, tools []ToolSpec, onDelta func(Delta)) (Turn, error) {
	type oaTool struct {
		Type     string `json:"type"`
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		} `json:"function"`
	}
	body := map[string]any{
		"model":          c.p.Model,
		"messages":       c.messages,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(tools) > 0 {
		var ts []oaTool
		for _, t := range tools {
			var o oaTool
			o.Type = "function"
			o.Function.Name, o.Function.Description, o.Function.Parameters = t.Name, t.Description, t.Schema
			ts = append(ts, o)
		}
		body["tools"] = ts
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Turn{}, err
	}
	url := strings.TrimRight(firstNonEmpty(c.p.BaseURL, "https://api.openai.com/v1"), "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return Turn{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.p.APIKey)
	}
	for k, v := range c.p.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Turn{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Turn{}, fmt.Errorf("%s returned %d: %s", c.p.Name(), resp.StatusCode, firstLine(errorMessage(b)))
	}

	type chunk struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				Reasoning        string `json:"reasoning"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens        int64 `json:"prompt_tokens"`
			CompletionTokens    int64 `json:"completion_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	var text strings.Builder
	calls := map[int]*oaToolCall{}
	finish := ""
	var usage Usage
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var ch chunk
		if json.Unmarshal([]byte(payload), &ch) != nil {
			continue
		}
		if ch.Usage != nil {
			usage.Input, usage.Output = ch.Usage.PromptTokens, ch.Usage.CompletionTokens
			if ch.Usage.PromptTokensDetails != nil {
				usage.CacheRead = ch.Usage.PromptTokensDetails.CachedTokens
			}
		}
		for _, choice := range ch.Choices {
			d := choice.Delta
			if r := d.Reasoning + d.ReasoningContent; r != "" {
				onDelta(Delta{Kind: "thinking", Text: r})
			}
			if d.Content != "" {
				text.WriteString(d.Content)
				onDelta(Delta{Kind: "text", Text: d.Content})
			}
			for _, tc := range d.ToolCalls {
				call := calls[tc.Index]
				if call == nil {
					call = &oaToolCall{Type: "function"}
					calls[tc.Index] = call
				}
				if tc.ID != "" {
					call.ID = tc.ID
				}
				call.Function.Name += tc.Function.Name
				call.Function.Arguments += tc.Function.Arguments
			}
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Turn{}, err
	}

	msg := oaMessage{Role: "assistant", Content: text.String()}
	turn := Turn{Text: text.String(), Usage: usage}
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for n, i := range idx {
		call := calls[i]
		if call.ID == "" {
			call.ID = fmt.Sprintf("call_%d", n)
		}
		if call.Function.Arguments == "" {
			call.Function.Arguments = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, *call)
		turn.Calls = append(turn.Calls, ToolCall{ID: call.ID, Name: call.Function.Name, Input: json.RawMessage(call.Function.Arguments)})
	}
	c.messages = append(c.messages, msg)
	switch {
	case finish == "length":
		turn.Stop = StopMaxTokens
	case finish == "content_filter":
		turn.Stop = StopRefusal
	case len(turn.Calls) > 0:
		turn.Stop = StopTools
	default:
		turn.Stop = StopEnd
	}
	return turn, nil
}

func errorMessage(b []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return string(b)
}
