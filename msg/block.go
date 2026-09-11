package msg

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type BlockType string

const (
	BlockText       BlockType = "text"
	BlockImage      BlockType = "image"
	BlockAudio      BlockType = "audio"
	BlockThinking   BlockType = "thinking"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

// Block 是受控的内容联合：Type 对应且仅对应一个非空字段。
// 使用构造函数创建，通过 Validate 检查；没有开放注册表。
type Block struct {
	Type       BlockType        `json:"type"`
	Text       *TextBlock       `json:"text,omitempty"`
	Image      *ImageBlock      `json:"image,omitempty"`
	Audio      *AudioBlock      `json:"audio,omitempty"`
	Thinking   *ThinkingBlock   `json:"thinking,omitempty"`
	ToolUse    *ToolUseBlock    `json:"tool_use,omitempty"`
	ToolResult *ToolResultBlock `json:"tool_result,omitempty"`
}
type TextBlock struct {
	Text string `json:"text"`
}
type ImageBlock struct {
	Source ImageSource `json:"source"`
}
type AudioBlock struct {
	Source AudioSource `json:"source"`
}
type Source struct {
	Kind      string `json:"kind"`
	URL       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}
type ImageSource = Source
type AudioSource = Source
type ThinkingBlock struct {
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
}

// Input 仅保存完整 JSON 对象。流式参数片段由 Aggregator 暂存。
type ToolUseBlock struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// Output 只允许 text/image/audio，不能嵌套工具结果或调用。
// IsError 表达已完成的失败结果；执行中的状态保留在执行器中。
type ToolResultBlock struct {
	ToolUseID string  `json:"tool_use_id"`
	Output    []Block `json:"output"`
	IsError   bool    `json:"is_error,omitempty"`
}

func Text(s string) Block  { return Block{Type: BlockText, Text: &TextBlock{Text: s}} }
func Image(s Source) Block { return Block{Type: BlockImage, Image: &ImageBlock{Source: s}} }
func Audio(s Source) Block { return Block{Type: BlockAudio, Audio: &AudioBlock{Source: s}} }
func Thinking(text, signature string) Block {
	return Block{Type: BlockThinking, Thinking: &ThinkingBlock{Thinking: text, Signature: signature}}
}
func ToolUse(id, name string, input json.RawMessage) Block {
	return Block{Type: BlockToolUse, ToolUse: &ToolUseBlock{ID: id, Name: name, Input: append(json.RawMessage(nil), input...)}}
}
func ToolResult(id string, output ...Block) Block {
	return Block{Type: BlockToolResult, ToolResult: &ToolResultBlock{ToolUseID: id, Output: output}}
}
func (s Source) validate(prefix string) error {
	switch s.Kind {
	case "url":
		u, err := url.Parse(s.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || s.Data != "" {
			return fmt.Errorf("source requires an HTTP(S) URL without inline data")
		}
	case "base64":
		if s.URL != "" || s.Data == "" || s.MediaType == "" {
			return fmt.Errorf("base64 source requires data and media_type, without URL")
		}
		if _, err := base64.StdEncoding.DecodeString(s.Data); err != nil {
			return fmt.Errorf("invalid base64: %w", err)
		}
	default:
		return fmt.Errorf("unknown source kind %q", s.Kind)
	}
	if s.MediaType != "" && !strings.HasPrefix(s.MediaType, prefix+"/") {
		return fmt.Errorf("expected %s media type", prefix)
	}
	return nil
}
func (b Block) Validate() error {
	n := 0
	for _, present := range []bool{b.Text != nil, b.Image != nil, b.Audio != nil, b.Thinking != nil, b.ToolUse != nil, b.ToolResult != nil} {
		if present {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("block must contain exactly one payload, got %d", n)
	}
	switch b.Type {
	case BlockText:
		if b.Text != nil {
			return nil
		}
	case BlockImage:
		if b.Image != nil {
			return b.Image.Source.validate("image")
		}
	case BlockAudio:
		if b.Audio != nil {
			return b.Audio.Source.validate("audio")
		}
	case BlockThinking:
		if b.Thinking != nil {
			return nil
		}
	case BlockToolUse:
		if b.ToolUse != nil {
			t := b.ToolUse
			if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Name) == "" {
				return fmt.Errorf("tool call requires id and name")
			}
			raw := strings.TrimSpace(string(t.Input))
			if !json.Valid(t.Input) || !strings.HasPrefix(raw, "{") {
				return fmt.Errorf("tool input must be a complete JSON object")
			}
			return nil
		}
	case BlockToolResult:
		if b.ToolResult != nil {
			if strings.TrimSpace(b.ToolResult.ToolUseID) == "" {
				return fmt.Errorf("tool result requires tool_use_id")
			}
			for i, out := range b.ToolResult.Output {
				if out.Type != BlockText && out.Type != BlockImage && out.Type != BlockAudio {
					return fmt.Errorf("output[%d]: only text/image/audio allowed", i)
				}
				if err := out.Validate(); err != nil {
					return fmt.Errorf("output[%d]: %w", i, err)
				}
			}
			return nil
		}
	}
	return fmt.Errorf("unknown block type or mismatched payload: %q", b.Type)
}
