package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// ListAnthropicModels lists the models an Anthropic API key can use, newest first.
func ListAnthropicModels(ctx context.Context, key, baseURL string) ([]string, error) {
	var opts []option.RequestOption
	if key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := anthropic.NewClient(opts...)
	pager := client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	var out []string
	for pager.Next() {
		out = append(out, pager.Current().ID)
	}
	if err := pager.Err(); err != nil {
		return nil, describeAnthropicError(err)
	}
	return out, nil
}

// ListOpenAIModels lists an OpenAI-compatible server's models (GET /models).
func ListOpenAIModels(ctx context.Context, baseURL, key string, headers map[string]string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%d: %s", resp.StatusCode, firstLine(errorMessage(b)))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		out = append(out, m.ID)
	}
	sort.Strings(out)
	return out, nil
}
