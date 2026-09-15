// Package anthropic implements a bounded subset of the native Messages API.
package anthropic

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
)

// MaxTokens zero defaults to 4096; cache-only requests are not supported.
type Formatter struct{ MaxTokens int }

var _ model.Formatter = Formatter{}

type block struct {
	Type      string            `json:"type"`
	Text      string            `json:"text,omitempty"`
	ID        string            `json:"id,omitempty"`
	Name      string            `json:"name,omitempty"`
	Input     json.RawMessage   `json:"input,omitempty"`
	ToolUseID string            `json:"tool_use_id,omitempty"`
	Content   []block           `json:"content,omitempty"`
	IsError   bool              `json:"is_error,omitempty"`
	Source    map[string]string `json:"source,omitempty"`
}

// Text blocks require text even when it is empty; do not emit unrelated fields.
func (b block) MarshalJSON() ([]byte, error) {
	if b.Type == "text" {
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{b.Type, b.Text})
	}
	type plain block
	return json.Marshal(plain(b))
}

type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}
type definition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}
type request struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    []block      `json:"system,omitempty"`
	Messages  []message    `json:"messages"`
	Tools     []definition `json:"tools,omitempty"`
	Stream    bool         `json:"stream"`
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func (f Formatter) Encode(r model.Request, name string, stream bool) ([]byte, error) {
	if strings.TrimSpace(name) == "" || len(r.Messages) == 0 || f.MaxTokens < 0 {
		return nil, fmt.Errorf("model, messages and nonnegative MaxTokens required")
	}
	if err := msg.ValidateConversation(r.Messages, true); err != nil {
		return nil, err
	}
	limit := f.MaxTokens
	if limit == 0 {
		limit = 4096
	}
	w := request{Model: name, MaxTokens: limit, Stream: stream}
	pending := map[string]bool{}
	for _, m := range r.Messages {
		if len(pending) > 0 && m.Role != msg.RoleTool {
			return nil, fmt.Errorf("tool results must immediately follow calls")
		}
		if m.Role == msg.RoleSystem {
			if len(w.Messages) > 0 {
				return nil, fmt.Errorf("only leading system messages supported")
			}
			for _, b := range m.Blocks {
				if b.Type != msg.BlockText {
					return nil, fmt.Errorf("system supports text only")
				}
				w.System = append(w.System, block{Type: "text", Text: b.Text.Text})
			}
			continue
		}
		wm := message{Role: string(m.Role)}
		if m.Role == msg.RoleTool {
			wm.Role = "user"
		}
		for _, b := range m.Blocks {
			switch b.Type {
			case msg.BlockText:
				wm.Content = append(wm.Content, block{Type: "text", Text: b.Text.Text})
			case msg.BlockImage:
				if m.Role != msg.RoleUser {
					return nil, fmt.Errorf("images supported only in user messages")
				}
				v, err := imageBlock(b)
				if err != nil {
					return nil, err
				}
				wm.Content = append(wm.Content, v)
			case msg.BlockToolUse:
				t := b.ToolUse
				if !validName.MatchString(t.Name) {
					return nil, fmt.Errorf("invalid tool name")
				}
				wm.Content = append(wm.Content, block{Type: "tool_use", ID: t.ID, Name: t.Name, Input: t.Input})
				pending[t.ID] = true
			case msg.BlockToolResult:
				t := b.ToolResult
				if !pending[t.ToolUseID] {
					return nil, fmt.Errorf("unexpected tool result")
				}
				delete(pending, t.ToolUseID)
				result := block{Type: "tool_result", ToolUseID: t.ToolUseID, IsError: t.IsError}
				for _, out := range t.Output {
					switch out.Type {
					case msg.BlockText:
						result.Content = append(result.Content, block{Type: "text", Text: out.Text.Text})
					case msg.BlockImage:
						v, err := imageBlock(out)
						if err != nil {
							return nil, err
						}
						result.Content = append(result.Content, v)
					default:
						return nil, fmt.Errorf("unsupported tool result modality")
					}
				}
				wm.Content = append(wm.Content, result)
			default:
				return nil, fmt.Errorf("unsupported input block %q", b.Type)
			}
		}
		// Merge adjacent turns, including separate messages carrying results of a batch.
		if n := len(w.Messages); n > 0 && w.Messages[n-1].Role == wm.Role {
			w.Messages[n-1].Content = append(w.Messages[n-1].Content, wm.Content...)
		} else {
			w.Messages = append(w.Messages, wm)
		}
	}
	if len(w.Messages) == 0 || w.Messages[0].Role != "user" || w.Messages[len(w.Messages)-1].Role != "user" {
		return nil, fmt.Errorf("conversation must begin and end with user/tool input; assistant prefill unsupported")
	}
	seen := map[string]bool{}
	for _, t := range r.Tools {
		var schema map[string]any
		if !validName.MatchString(t.Name) || seen[t.Name] || json.Unmarshal(t.Parameters, &schema) != nil || schema["type"] != "object" {
			return nil, fmt.Errorf("invalid tool definition")
		}
		seen[t.Name] = true
		w.Tools = append(w.Tools, definition{t.Name, t.Description, t.Parameters})
	}
	return json.Marshal(w)
}
func imageBlock(b msg.Block) (block, error) {
	s := b.Image.Source
	source := map[string]string{"type": s.Kind}
	if s.Kind == "url" {
		source["url"] = s.URL
	} else {
		switch s.MediaType {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			return block{}, fmt.Errorf("unsupported image media type")
		}
		source["media_type"], source["data"] = s.MediaType, s.Data
	}
	return block{Type: "image", Source: source}, nil
}

type usage struct {
	Input       int `json:"input_tokens"`
	Output      int `json:"output_tokens"`
	CacheRead   int `json:"cache_read_input_tokens"`
	CacheCreate int `json:"cache_creation_input_tokens"`
}

func (u usage) convert() (*model.Usage, error) {
	if u.Input < 0 || u.Output < 0 || u.CacheRead < 0 || u.CacheCreate < 0 {
		return nil, fmt.Errorf("negative token usage")
	}
	prompt := u.Input + u.CacheRead + u.CacheCreate
	return &model.Usage{PromptTokens: prompt, CompletionTokens: u.Output, TotalTokens: prompt + u.Output}, nil
}

type envelope struct {
	Type       string  `json:"type"`
	ID         string  `json:"id"`
	Role       string  `json:"role"`
	Model      string  `json:"model"`
	Content    []block `json:"content"`
	StopReason string  `json:"stop_reason"`
	Usage      usage   `json:"usage"`
}

func finish(reason string, calls int) error {
	if reason != "end_turn" && reason != "tool_use" {
		return fmt.Errorf("incomplete or unsupported stop reason %q", reason)
	}
	if (reason == "tool_use") != (calls > 0) {
		return fmt.Errorf("stop reason and tool calls disagree")
	}
	return nil
}
func decodeBlock(b block) (msg.Block, error) {
	switch b.Type {
	case "text":
		return msg.Text(b.Text), nil
	case "tool_use":
		if !validName.MatchString(b.Name) {
			return msg.Block{}, fmt.Errorf("invalid tool name")
		}
		return msg.ToolUse(b.ID, b.Name, b.Input), nil
	default:
		return msg.Block{}, fmt.Errorf("unsupported output block %q", b.Type)
	}
}
func (f Formatter) Decode(raw []byte) (*model.Response, error) {
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, fmt.Errorf("invalid response JSON: %w", err)
	}
	if e.Type != "message" || e.Role != "assistant" || e.ID == "" {
		return nil, fmt.Errorf("expected assistant message")
	}
	m := msg.New("assistant", msg.RoleAssistant)
	calls := 0
	for _, b := range e.Content {
		v, err := decodeBlock(b)
		if err != nil {
			return nil, err
		}
		m.Add(v)
		if b.Type == "tool_use" {
			calls++
		}
	}
	if err := finish(e.StopReason, calls); err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	u, err := e.Usage.convert()
	if err != nil {
		return nil, err
	}
	return &model.Response{Message: m, ID: e.ID, Model: e.Model, FinishReason: e.StopReason, Usage: u}, nil
}
