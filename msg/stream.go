package msg

import (
	"encoding/json"
	"fmt"
)

type EventType string

const (
	BlockStart EventType = "block_start"
	BlockDelta EventType = "block_delta"
	BlockEnd   EventType = "block_end"
)

// StreamEvent 是适配器归一化后的有序事件。BlockID 在一条回复内唯一。
// Start 使用 Block（文本/思考/工具调用）；Delta 使用 Delta；End 不携带数据。
// 传输层负责去重与排序；聚合器不推测重复 delta 是否应忽略。
type StreamEvent struct {
	Type    EventType
	BlockID string
	Block   Block
	Delta   string
}
type partialBlock struct {
	id    string
	block Block
	data  string
	ended bool
}

// Aggregator 每个实例处理一条 assistant 回复；非并发安全。
// 按 Start 顺序输出，允许不同块的增量交错。完整图片等通过 AddBlock 加入。
type Aggregator struct {
	message  *Msg
	parts    []partialBlock
	indexes  map[string]int
	finished bool
}

func NewAggregator(name string) *Aggregator {
	return &Aggregator{message: New(name, RoleAssistant), indexes: map[string]int{}}
}

// AddBlock 加入已完成的非流式块，复制调用方数据，保持与流式块的顺序。
func (a *Aggregator) AddBlock(id string, b Block) error {
	if a == nil || a.message == nil || a.finished {
		return fmt.Errorf("aggregator is not open")
	}
	if id == "" {
		return fmt.Errorf("empty block ID")
	}
	if _, ok := a.indexes[id]; ok {
		return fmt.Errorf("duplicate block ID %q", id)
	}
	c, err := New(a.message.Name, RoleAssistant, b).Clone()
	if err != nil {
		return err
	}
	a.indexes[id] = len(a.parts)
	a.parts = append(a.parts, partialBlock{id: id, block: c.Blocks[0], ended: true})
	return nil
}
func (a *Aggregator) Apply(e StreamEvent) error {
	if a == nil || a.message == nil || a.finished {
		return fmt.Errorf("aggregator is not open")
	}
	if e.BlockID == "" {
		return fmt.Errorf("empty block ID")
	}
	index, exists := a.indexes[e.BlockID]
	switch e.Type {
	case BlockStart:
		if exists {
			return fmt.Errorf("duplicate block start %q", e.BlockID)
		}
		if e.Delta != "" {
			return fmt.Errorf("start cannot carry delta")
		}
		b := e.Block
		var data string
		switch b.Type {
		case BlockText:
			if b.Text != nil {
				data = b.Text.Text
			}
		case BlockThinking:
			if b.Thinking != nil {
				data = b.Thinking.Thinking
			}
		case BlockToolUse:
			if b.ToolUse != nil {
				data = string(b.ToolUse.Input)
				// Start 中的参数可以不完整。只校验其他字段，临时替换参数副本。
				v := *b.ToolUse
				v.Input = json.RawMessage(`{}`)
				b.ToolUse = &v
			}
		default:
			return fmt.Errorf("block type %q does not support deltas", b.Type)
		}
		c, err := New(a.message.Name, RoleAssistant, b).Clone()
		if err != nil {
			return err
		}
		a.indexes[e.BlockID] = len(a.parts)
		a.parts = append(a.parts, partialBlock{id: e.BlockID, block: c.Blocks[0], data: data})
		return nil
	case BlockDelta, BlockEnd:
		if !exists {
			return fmt.Errorf("unknown block ID %q", e.BlockID)
		}
		p := &a.parts[index]
		if p.ended {
			return fmt.Errorf("block %q already ended", e.BlockID)
		}
		if e.Block != (Block{}) {
			return fmt.Errorf("delta/end cannot carry a block")
		}
		if e.Type == BlockDelta {
			p.data += e.Delta
			return nil
		}
		if e.Delta != "" {
			return fmt.Errorf("end cannot carry delta")
		}
		b := p.block
		switch b.Type {
		case BlockText:
			b = Text(p.data)
		case BlockThinking:
			b = Thinking(p.data, b.Thinking.Signature)
		case BlockToolUse:
			b = ToolUse(b.ToolUse.ID, b.ToolUse.Name, json.RawMessage(p.data))
		}
		if err := b.Validate(); err != nil {
			return fmt.Errorf("end block %q: %w", e.BlockID, err)
		}
		p.block = b
		p.ended = true
		return nil
	default:
		return fmt.Errorf("unknown event type %q", e.Type)
	}
}

// Finish 只返回已结束且合法的完整消息。失败不关闭聚合器，可继续接收增量。
// 成功后关闭；后续事件和重复 Finish 返回错误。
func (a *Aggregator) Finish() (*Msg, error) {
	if a == nil || a.message == nil || a.finished {
		return nil, fmt.Errorf("aggregator is not open")
	}
	candidate := *a.message
	candidate.Blocks = make([]Block, len(a.parts))
	for i, p := range a.parts {
		if !p.ended {
			return nil, fmt.Errorf("block %q is not ended", p.id)
		}
		candidate.Blocks[i] = p.block
	}
	out, err := candidate.Clone()
	if err != nil {
		return nil, err
	}
	a.finished = true
	return out, nil
}
