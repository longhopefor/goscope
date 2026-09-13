// Package openai 实现 Chat Completions 的受限适配，不代表所有兼容服务的完整支持。
package openai

import (
	"encoding/json"
	"fmt"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"regexp"
	"strings"
)

type Formatter struct{}

var _ model.Formatter = Formatter{}

type function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}
type toolCall struct {
	Index    *int     `json:"index,omitempty"`
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function function `json:"function"`
}
type wireMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}
type wireRequest struct {
	Model         string        `json:"model"`
	Messages      []wireMessage `json:"messages"`
	Tools         []wireTool    `json:"tools,omitempty"`
	Stream        bool          `json:"stream"`
	StreamOptions any           `json:"stream_options,omitempty"`
}

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func (Formatter) Encode(r model.Request, name string, stream bool) ([]byte, error) {
	if strings.TrimSpace(name) == "" || len(r.Messages) == 0 {
		return nil, fmt.Errorf("model and messages are required")
	}
	if err := msg.ValidateConversation(r.Messages, true); err != nil {
		return nil, err
	}
	w := wireRequest{Model: name, Stream: stream}
	if stream {
		w.StreamOptions = map[string]bool{"include_usage": true}
	}
	// Chat Completions 要求 tool 结果紧随对应 assistant 调用，多个结果可乱序。
	pending := map[string]bool{}
	for i, m := range r.Messages {
		if len(pending) > 0 && m.Role != msg.RoleTool {
			return nil, fmt.Errorf("message %d interrupts pending tool results", i)
		}
		if m.Role == msg.RoleTool {
			for _, b := range m.Blocks {
				t := b.ToolResult
				if !pending[t.ToolUseID] {
					return nil, fmt.Errorf("unexpected tool result %q", t.ToolUseID)
				}
				var texts []string
				for _, out := range t.Output {
					if out.Type != msg.BlockText {
						return nil, fmt.Errorf("Chat Completions adapter only supports text tool results")
					}
					texts = append(texts, out.Text.Text)
				}
				content := strings.Join(texts, "\n")
				if t.IsError {
					content = "Tool execution failed: " + content
				}
				w.Messages = append(w.Messages, wireMessage{Role: "tool", ToolCallID: t.ToolUseID, Content: content})
				delete(pending, t.ToolUseID)
			}
			continue
		}
		wm := wireMessage{Role: string(m.Role)}
		var parts []map[string]any
		var texts []string
		images := false
		callsStarted := false
		for _, b := range m.Blocks {
			switch b.Type {
			case msg.BlockText:
				if callsStarted {
					return nil, fmt.Errorf("text after tool calls cannot preserve block order")
				}
				texts = append(texts, b.Text.Text)
				parts = append(parts, map[string]any{"type": "text", "text": b.Text.Text})
			case msg.BlockImage:
				if m.Role != msg.RoleUser {
					return nil, fmt.Errorf("only user images supported")
				}
				s := b.Image.Source
				u := s.URL
				if s.Kind == "base64" {
					u = "data:" + s.MediaType + ";base64," + s.Data
				}
				images = true
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": u}})
			case msg.BlockToolUse:
				callsStarted = true
				t := b.ToolUse
				wm.ToolCalls = append(wm.ToolCalls, toolCall{ID: t.ID, Type: "function", Function: function{Name: t.Name, Arguments: string(t.Input)}})
				pending[t.ID] = true
			default:
				return nil, fmt.Errorf("unsupported block %q in Chat Completions adapter", b.Type)
			}
		}
		if images {
			wm.Content = parts
		} else if len(texts) > 0 {
			wm.Content = strings.Join(texts, "\n")
		}
		w.Messages = append(w.Messages, wm)
	}
	seen := map[string]bool{}
	for _, t := range r.Tools {
		if !toolName.MatchString(t.Name) || seen[t.Name] {
			return nil, fmt.Errorf("invalid or duplicate tool name %q", t.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(t.Parameters, &schema); err != nil || schema["type"] != "object" {
			return nil, fmt.Errorf("tool %q parameters must be an object schema", t.Name)
		}
		seen[t.Name] = true
		wt := wireTool{Type: "function"}
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.Parameters
		w.Tools = append(w.Tools, wt)
	}
	return json.Marshal(w)
}

type responseMessage struct {
	Role      string          `json:"role"`
	Content   *string         `json:"content"`
	Refusal   *string         `json:"refusal"`
	ToolCalls []toolCall      `json:"tool_calls"`
	Audio     json.RawMessage `json:"audio"`
	Reasoning json.RawMessage `json:"reasoning_content"`
}
type choice struct {
	Index        int             `json:"index"`
	Message      responseMessage `json:"message"`
	Delta        responseMessage `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
}
type envelope struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Choices []choice        `json:"choices"`
	Usage   *model.Usage    `json:"usage"`
	Error   json.RawMessage `json:"error"`
}

func present(r json.RawMessage) bool { return len(r) > 0 && string(r) != "null" }
func checkContent(m responseMessage) error {
	if m.Refusal != nil && *m.Refusal != "" {
		return fmt.Errorf("model refused the request")
	}
	if present(m.Audio) || present(m.Reasoning) {
		return fmt.Errorf("unsupported model output modality")
	}
	return nil
}
func finishValid(reason string, calls int) error {
	if reason != "stop" && reason != "tool_calls" {
		return fmt.Errorf("incomplete or unsupported finish reason %q", reason)
	}
	if (reason == "tool_calls") != (calls > 0) {
		return fmt.Errorf("finish reason and tool calls disagree")
	}
	return nil
}
func (Formatter) Decode(raw []byte) (*model.Response, error) {
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if present(e.Error) {
		return nil, fmt.Errorf("provider returned an error envelope")
	}
	if len(e.Choices) != 1 || e.Choices[0].Index != 0 {
		return nil, fmt.Errorf("expected one choice at index 0")
	}
	c := e.Choices[0]
	m := c.Message
	if m.Role != "assistant" {
		return nil, fmt.Errorf("expected assistant response")
	}
	if err := checkContent(m); err != nil {
		return nil, err
	}
	if c.FinishReason == nil {
		return nil, fmt.Errorf("missing finish reason")
	}
	if err := finishValid(*c.FinishReason, len(m.ToolCalls)); err != nil {
		return nil, err
	}
	out := msg.New("assistant", msg.RoleAssistant)
	if m.Content != nil {
		out.Add(msg.Text(*m.Content))
	}
	for _, t := range m.ToolCalls {
		if t.Type != "function" {
			return nil, fmt.Errorf("unsupported tool call type %q", t.Type)
		}
		out.Add(msg.ToolUse(t.ID, t.Function.Name, json.RawMessage(t.Function.Arguments)))
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return &model.Response{Message: out, ID: e.ID, Model: e.Model, FinishReason: *c.FinishReason, Usage: e.Usage}, nil
}
