package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bright/reviewagent/internal/domain"
	"github.com/bright/reviewagent/internal/review"
)

const maxToolCalls = 4

type OpenAIReviewer struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
	tools   review.ToolExecutor
}

func NewOpenAIReviewer(baseURL, apiKey, model string) *OpenAIReviewer {
	return &OpenAIReviewer{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, model: model, client: &http.Client{Timeout: 45 * time.Second}}
}

func (r *OpenAIReviewer) WithTools(executor review.ToolExecutor) review.Reviewer {
	clone := *r
	clone.tools = executor
	return &clone
}

func (r *OpenAIReviewer) ReviewFile(ctx context.Context, file domain.ChangedFile) ([]domain.Finding, error) {
	messages := []map[string]any{
		{"role": "system", "content": "You are a conservative code reviewer. Find only correctness, error handling, security, concurrency, or resource-leak issues. Ignore style. Return only valid JSON when you finish."},
		{"role": "user", "content": reviewPrompt(file)},
	}
	toolCalls := 0
	for {
		response, err := r.complete(ctx, messages)
		if err != nil {
			return nil, err
		}
		if len(response.ToolCalls) == 0 {
			return decodeFindings(response.Content)
		}
		if r.tools == nil {
			return nil, fmt.Errorf("model requested a tool but no tool executor is configured")
		}
		if toolCalls+len(response.ToolCalls) > maxToolCalls {
			return nil, fmt.Errorf("tool-call budget exhausted (%d)", maxToolCalls)
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": response.Content, "tool_calls": response.ToolCalls})
		for _, call := range response.ToolCalls {
			result, callErr := r.tools.Call(ctx, call.Function.Name, json.RawMessage(call.Function.Arguments))
			if callErr != nil {
				result = "tool error: " + callErr.Error()
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": result})
		}
		toolCalls += len(response.ToolCalls)
	}
}

func reviewPrompt(file domain.ChangedFile) string {
	return fmt.Sprintf(`Review this changed file. Use tools only when additional repository context is needed: read_diff for another PR patch, list_files for a bounded tree view, search_code to find relevant source references, and read_file to inspect an exact range. Do not speculate; every issue must be supported by the diff or tool output.
Return a JSON object exactly shaped as {"findings":[...]}. Each finding must have title, body, severity (critical|high|medium|low), confidence (0-1), file, new_line, evidence (array), suggestion. new_line must be an added line in this diff.

FILE: %s
DIFF:
%s`, file.Path, file.Patch)
}

type completion struct {
	Content   string
	ToolCalls []toolCall
}
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (r *OpenAIReviewer) complete(ctx context.Context, messages []map[string]any) (completion, error) {
	payload := map[string]any{"model": r.model, "messages": messages, "response_format": map[string]string{"type": "json_object"}}
	if r.tools != nil {
		payload["tools"] = toolDefinitions()
		payload["tool_choice"] = "auto"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return completion{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return completion{}, err
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return completion{}, fmt.Errorf("call LLM: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return completion{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return completion{}, fmt.Errorf("LLM returned %s: %s", resp.Status, string(responseBody))
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []toolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return completion{}, fmt.Errorf("decode LLM response: %w", err)
	}
	if len(response.Choices) == 0 {
		return completion{}, fmt.Errorf("LLM returned no choices")
	}
	return completion{Content: response.Choices[0].Message.Content, ToolCalls: response.Choices[0].Message.ToolCalls}, nil
}

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{"type": "function", "function": map[string]any{
			"name": "read_file", "description": "Read a small line range from a repository file at the PR head commit.",
			"parameters": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path", "start_line", "end_line"}, "properties": map[string]any{
				"path":       map[string]any{"type": "string", "description": "Repository-relative slash-separated path."},
				"start_line": map[string]any{"type": "integer", "minimum": 1},
				"end_line":   map[string]any{"type": "integer", "minimum": 1},
			}},
		}},
		{"type": "function", "function": map[string]any{
			"name": "read_diff", "description": "Read the PR patch for one changed file, or all available patches when path is omitted.",
			"parameters": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Optional changed-file path."},
			}},
		}},
		{"type": "function", "function": map[string]any{
			"name": "list_files", "description": "List up to 100 repository files at the PR head commit, optionally under one prefix.",
			"parameters": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"path_prefix": map[string]any{"type": "string", "description": "Optional repository-relative directory prefix."},
			}},
		}},
		{"type": "function", "function": map[string]any{
			"name": "search_code", "description": "Case-insensitive text search through a bounded set of source files at the PR head commit.",
			"parameters": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
				"query":       map[string]any{"type": "string", "description": "Identifier or text to search for."},
				"path_prefix": map[string]any{"type": "string", "description": "Optional repository-relative directory prefix."},
			}},
		}},
	}
}

func decodeFindings(content string) ([]domain.Finding, error) {
	var result struct {
		Findings []domain.Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("decode findings JSON: %w", err)
	}
	return result.Findings, nil
}
