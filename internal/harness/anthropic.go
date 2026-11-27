package harness

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Anthropic talks to Claude through the Anthropic Messages API.
type Anthropic struct {
	Model   string
	APIKey  string
	BaseURL string
	// Effort is low, medium, high (default), xhigh or max, on models that take it.
	Effort string
	client *anthropic.Client
}

func (p *Anthropic) Name() string { return "anthropic/" + p.Model }

func (p *Anthropic) New(system string) Conversation {
	if p.client == nil {
		var opts []option.RequestOption
		if p.APIKey != "" {
			opts = append(opts, option.WithAPIKey(p.APIKey))
		}
		if p.BaseURL != "" {
			opts = append(opts, option.WithBaseURL(p.BaseURL))
		}
		c := anthropic.NewClient(opts...)
		p.client = &c
	}
	return &anthropicConv{p: p, system: system}
}

// Models from Claude 4.6 on take adaptive thinking and effort; these also take the
// server-side refusal fallback.
var (
	modernClaude   = regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable|mythos)-(5|4-[6-9])`)
	fallbackClaude = regexp.MustCompile(`^claude-(fable-5-[12]|opus-5-5|opus-5|sonnet-5-5)$`)
)

type anthropicConv struct {
	p        *Anthropic
	system   string
	messages []anthropic.BetaMessageParam
}

func (c *anthropicConv) appendUser(blocks ...anthropic.BetaContentBlockParamUnion) {
	if n := len(c.messages); n > 0 && c.messages[n-1].OfUser != nil {
		u := c.messages[n-1].OfUser
		u.Content = append(u.Content, blocks...)
		return
	}
	c.messages = append(c.messages, anthropic.NewBetaUserMessage(blocks...))
}

func (c *anthropicConv) User(text string) { c.appendUser(anthropic.NewBetaTextBlock(text)) }

func (c *anthropicConv) Results(results []ToolResult, note string) {
	blocks := make([]anthropic.BetaContentBlockParamUnion, 0, len(results)+1)
	for _, r := range results {
		blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.ID, r.Content, r.IsError))
	}
	if note != "" {
		blocks = append(blocks, anthropic.NewBetaTextBlock(note))
	}
	c.appendUser(blocks...)
}

func (c *anthropicConv) Step(ctx context.Context, tools []ToolSpec, onDelta func(Delta)) (Turn, error) {
	model := c.p.Model
	params := anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(model),
		MaxTokens:    64000,
		Messages:     c.messages,
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		Betas:        []anthropic.AnthropicBeta{anthropic.AnthropicBetaCompact2026_01_12},
		ContextManagement: anthropic.BetaContextManagementConfigParam{
			Edits: []anthropic.BetaContextManagementConfigEditUnionParam{
				{OfCompact20260112: &anthropic.BetaCompact20260112EditParam{}},
			},
		},
	}
	if c.system != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: c.system}}
	}
	if modernClaude.MatchString(model) {
		params.Thinking = anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{
			Display: anthropic.BetaThinkingConfigAdaptiveDisplaySummarized,
		}}
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(firstNonEmpty(c.p.Effort, "high"))}
	}
	if fallbackClaude.MatchString(model) {
		// A request a safety classifier declines is re-served by a fallback model.
		params.Betas = append(params.Betas, anthropic.AnthropicBetaServerSideFallback2026_07_01)
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}
	for _, t := range tools {
		params.Tools = append(params.Tools, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.BetaToolInputSchemaParam{
				Properties: t.Schema["properties"],
				Required:   requiredOf(t.Schema),
			},
		}})
	}

	stream := c.p.client.Beta.Messages.NewStreaming(ctx, params)
	msg := anthropic.BetaMessage{}
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return Turn{}, err
		}
		if d, ok := ev.AsAny().(anthropic.BetaRawContentBlockDeltaEvent); ok {
			switch v := d.Delta.AsAny().(type) {
			case anthropic.BetaTextDelta:
				onDelta(Delta{Kind: "text", Text: v.Text})
			case anthropic.BetaThinkingDelta:
				onDelta(Delta{Kind: "thinking", Text: v.Thinking})
			}
		}
	}
	if err := stream.Err(); err != nil {
		return Turn{}, describeAnthropicError(err)
	}
	c.messages = append(c.messages, msg.ToParam())

	turn := Turn{Usage: Usage{Input: msg.Usage.InputTokens, Output: msg.Usage.OutputTokens, CacheRead: msg.Usage.CacheReadInputTokens}}
	var text strings.Builder
	for _, b := range msg.Content {
		switch v := b.AsAny().(type) {
		case anthropic.BetaTextBlock:
			text.WriteString(v.Text)
		case anthropic.BetaToolUseBlock:
			turn.Calls = append(turn.Calls, ToolCall{ID: v.ID, Name: v.Name, Input: []byte(v.JSON.Input.Raw())})
		}
	}
	turn.Text = text.String()
	switch msg.StopReason {
	case anthropic.BetaStopReasonToolUse:
		turn.Stop = StopTools
	case anthropic.BetaStopReasonEndTurn, anthropic.BetaStopReasonStopSequence:
		turn.Stop = StopEnd
	case anthropic.BetaStopReasonMaxTokens:
		turn.Stop = StopMaxTokens
	case anthropic.BetaStopReasonRefusal:
		turn.Stop = StopRefusal
		turn.Detail = msg.StopDetails.Explanation
	default:
		turn.Stop, turn.Detail = StopOther, string(msg.StopReason)
	}
	return turn, nil
}

func describeAnthropicError(err error) error {
	var apierr *anthropic.Error
	if errors.As(err, &apierr) {
		switch apierr.StatusCode {
		case 401:
			return fmt.Errorf("the Anthropic API rejected the key (401). Set a new one: rix secret set ANTHROPIC_API_KEY")
		case 429:
			return fmt.Errorf("rate limited by the Anthropic API (429); try again shortly")
		}
		return fmt.Errorf("Anthropic API error %d: %s", apierr.StatusCode, firstLine(apierr.Error()))
	}
	return err
}

func requiredOf(schema map[string]any) []string {
	switch r := schema["required"].(type) {
	case []string:
		return r
	case []any:
		out := make([]string, 0, len(r))
		for _, v := range r {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
