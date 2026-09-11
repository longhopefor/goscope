package msg

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Msg 表示完整消息。构造和 Add 不自动校验，序列化及消费边界使用 Validate。
// 消息和块是可变对象，不保证并发安全；共享前 Clone 或约定只读。
type Msg struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
	// 元数据限定为 JSON，避免任意 Go 对象无法可靠复制或持久化。
	Metadata  map[string]json.RawMessage `json:"metadata,omitempty"`
	Timestamp time.Time                  `json:"timestamp"`
}

// NewID 使用随机标识，不提供时间顺序保证。
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("generate message ID: %w", err))
	}
	return hex.EncodeToString(b[:])
}
func New(name string, role Role, blocks ...Block) *Msg {
	return &Msg{ID: NewID(), Name: name, Role: role, Blocks: blocks, Timestamp: time.Now()}
}
func NewText(name string, role Role, text string) *Msg { return New(name, role, Text(text)) }
func (m *Msg) Add(b Block) *Msg                        { m.Blocks = append(m.Blocks, b); return m }
func (m *Msg) Text() string {
	if m == nil {
		return ""
	}
	if len(m.Blocks) == 1 && m.Blocks[0].Type == BlockText && m.Blocks[0].Text != nil {
		return m.Blocks[0].Text.Text
	}
	var texts []string
	for _, b := range m.Blocks {
		if b.Type == BlockText && b.Text != nil {
			texts = append(texts, b.Text.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// BlocksOfType 返回借用的块，修改其指针字段会影响原消息。
func (m *Msg) BlocksOfType(t BlockType) []Block {
	var out []Block
	if m != nil {
		for _, b := range m.Blocks {
			if b.Type == t {
				out = append(out, b)
			}
		}
	}
	return out
}
func (m *Msg) String() string {
	if m == nil {
		return "<nil>"
	}
	r := []rune(m.Text())
	if len(r) > 80 {
		r = append(r[:80], []rune("...")...)
	}
	return fmt.Sprintf("[%s/%s] %s", m.Role, m.Name, string(r))
}
func (m *Msg) Validate() error {
	if m == nil {
		return fmt.Errorf("nil message")
	}
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.Name) == "" || m.Timestamp.IsZero() {
		return fmt.Errorf("message requires id, name and timestamp")
	}
	switch m.Role {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
	default:
		return fmt.Errorf("invalid role %q", m.Role)
	}
	if len(m.Blocks) == 0 {
		return fmt.Errorf("complete message requires at least one block")
	}
	calls := map[string]bool{}
	results := map[string]bool{}
	for i, b := range m.Blocks {
		if err := b.Validate(); err != nil {
			return fmt.Errorf("blocks[%d]: %w", i, err)
		}
		allowed := false
		switch m.Role {
		case RoleSystem:
			allowed = b.Type == BlockText
		case RoleUser:
			allowed = b.Type == BlockText || b.Type == BlockImage || b.Type == BlockAudio
		case RoleAssistant:
			allowed = b.Type != BlockToolResult
		case RoleTool:
			allowed = b.Type == BlockToolResult
		}
		if !allowed {
			return fmt.Errorf("blocks[%d]: %s not allowed for %s", i, b.Type, m.Role)
		}
		if b.ToolUse != nil {
			id := b.ToolUse.ID
			if calls[id] {
				return fmt.Errorf("duplicate tool call %q", id)
			}
			calls[id] = true
		}
		if b.ToolResult != nil {
			id := b.ToolResult.ToolUseID
			if results[id] {
				return fmt.Errorf("duplicate tool result %q", id)
			}
			results[id] = true
		}
	}
	for k, v := range m.Metadata {
		if !json.Valid(v) {
			return fmt.Errorf("metadata[%q]: invalid JSON", k)
		}
	}
	return nil
}

// ValidateConversation 检查完整历史中的调用/结果配对，不规定厂商角色交替。
// requireResolved=false 允许历史以等待工具执行的助手消息结束。
func ValidateConversation(messages []*Msg, requireResolved bool) error {
	calls := map[string]bool{}
	pending := map[string]bool{}
	for i, m := range messages {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("messages[%d]: %w", i, err)
		}
		for _, b := range m.Blocks {
			if b.ToolUse != nil {
				id := b.ToolUse.ID
				if calls[id] {
					return fmt.Errorf("duplicate tool call %q", id)
				}
				calls[id] = true
				pending[id] = true
			}
			if b.ToolResult != nil {
				id := b.ToolResult.ToolUseID
				if !pending[id] {
					return fmt.Errorf("unmatched or duplicate tool result %q", id)
				}
				delete(pending, id)
			}
		}
	}
	if requireResolved && len(pending) > 0 {
		return fmt.Errorf("%d unresolved tool calls", len(pending))
	}
	return nil
}

// msgJSON 避免 MarshalJSON/UnmarshalJSON 递归调用自身。
type msgJSON Msg

func (m Msg) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(msgJSON(m))
}

// 解码并验证成功后才替换接收者，失败不会留下半条消息。
func (m *Msg) UnmarshalJSON(data []byte) error {
	var w msgJSON
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	candidate := Msg(w)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*m = candidate
	return nil
}

// Clone 对所有 JSON 数据深拷贝，失败显式返回错误，绝不回退共享引用。
func (m *Msg) Clone() (*Msg, error) {
	if m == nil {
		return nil, fmt.Errorf("cannot clone nil message")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out Msg
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
