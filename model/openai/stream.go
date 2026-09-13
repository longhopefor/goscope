package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"io"
	"strings"
)

// readSSE 按空行分帧，支持 CRLF、注释、多个 data 行；EOF 不等价于正常完成。
func readSSE(r io.Reader, consume func(string) (bool, error)) error {
	s := bufio.NewScanner(io.LimitReader(r, maxBody+1))
	s.Buffer(make([]byte, 4096), 1<<20)
	var lines []string
	total := 0
	for s.Scan() {
		line := s.Text()
		total += len(line) + 1
		if total > maxBody {
			return fmt.Errorf("stream exceeds size limit")
		}
		if line == "" {
			if len(lines) > 0 {
				done, err := consume(strings.Join(lines, "\n"))
				if err != nil {
					return err
				}
				if done {
					return nil
				}
				lines = nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			lines = append(lines, value)
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	return fmt.Errorf("stream ended without [DONE]: %w", io.ErrUnexpectedEOF)
}
func (c *Client) Stream(ctx context.Context, r model.Request, emit func(msg.StreamEvent) error) (*model.Response, error) {
	res, err := c.send(ctx, r, true)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if !strings.HasPrefix(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream") {
		return nil, fmt.Errorf("expected text/event-stream")
	}
	a := msg.NewAggregator("assistant")
	apply := func(e msg.StreamEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.Apply(e); err != nil {
			return err
		}
		if emit != nil {
			return emit(e)
		}
		return nil
	}
	out := &model.Response{}
	calls := map[int]*toolCall{}
	var order []int
	textStarted := false
	finished := false
	err = readSSE(res.Body, func(data string) (bool, error) {
		if data == "[DONE]" {
			if !finished {
				return false, fmt.Errorf("[DONE] before finish reason")
			}
			return true, nil
		}
		var e envelope
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return false, fmt.Errorf("invalid stream JSON: %w", err)
		}
		if present(e.Error) {
			return false, fmt.Errorf("provider returned stream error")
		}
		if e.ID != "" {
			if out.ID != "" && out.ID != e.ID {
				return false, fmt.Errorf("stream response ID changed")
			}
			out.ID = e.ID
		}
		if e.Model != "" {
			out.Model = e.Model
		}
		if e.Usage != nil {
			out.Usage = e.Usage
		}
		if len(e.Choices) == 0 {
			if e.Usage == nil {
				return false, fmt.Errorf("empty stream chunk")
			}
			return false, nil
		}
		if finished {
			return false, fmt.Errorf("choice received after finish reason")
		}
		if len(e.Choices) != 1 || e.Choices[0].Index != 0 {
			return false, fmt.Errorf("expected one stream choice at index 0")
		}
		ch := e.Choices[0]
		d := ch.Delta
		if d.Role != "" && d.Role != "assistant" {
			return false, fmt.Errorf("unexpected stream role")
		}
		if err := checkContent(d); err != nil {
			return false, err
		}
		if d.Content != nil {
			if !textStarted {
				if err := apply(msg.StreamEvent{Type: msg.BlockStart, BlockID: "text", Block: msg.Text("")}); err != nil {
					return false, err
				}
				textStarted = true
			}
			if err := apply(msg.StreamEvent{Type: msg.BlockDelta, BlockID: "text", Delta: *d.Content}); err != nil {
				return false, err
			}
		}
		for _, delta := range d.ToolCalls {
			if delta.Index == nil || *delta.Index < 0 {
				return false, fmt.Errorf("tool delta requires nonnegative index")
			}
			i := *delta.Index
			t := calls[i]
			if t == nil {
				t = &toolCall{}
				calls[i] = t
				order = append(order, i)
			}
			if delta.Type != "" {
				if delta.Type != "function" {
					return false, fmt.Errorf("unsupported tool delta type")
				}
				t.Type = delta.Type
			}
			// Chat Completions 的 ID 是标识，不是参数增量；重复时必须相同。
			if delta.ID != "" {
				if t.ID != "" && t.ID != delta.ID {
					return false, fmt.Errorf("tool call ID changed")
				}
				t.ID = delta.ID
			}
			t.Function.Name += delta.Function.Name
			t.Function.Arguments += delta.Function.Arguments
		}
		if ch.FinishReason != nil {
			if err := finishValid(*ch.FinishReason, len(calls)); err != nil {
				return false, err
			}
			out.FinishReason = *ch.FinishReason
			finished = true
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	// 文本实时发出，工具参数按 index 缓冲，在确认流结束后发送完整块。
	// 这是 Chat Completions 分离 content/tool_calls 字段的归一化顺序。
	if textStarted {
		if err := apply(msg.StreamEvent{Type: msg.BlockEnd, BlockID: "text"}); err != nil {
			return nil, err
		}
	}
	for _, i := range order {
		t := calls[i]
		if t.Type != "function" {
			return nil, fmt.Errorf("missing function tool type")
		}
		b := msg.ToolUse(t.ID, t.Function.Name, json.RawMessage(t.Function.Arguments))
		if err := b.Validate(); err != nil {
			return nil, err
		}
		id := fmt.Sprintf("tool/%d", i)
		for _, event := range []msg.StreamEvent{
			{Type: msg.BlockStart, BlockID: id, Block: msg.ToolUse(t.ID, t.Function.Name, nil)},
			{Type: msg.BlockDelta, BlockID: id, Delta: t.Function.Arguments},
			{Type: msg.BlockEnd, BlockID: id},
		} {
			if err := apply(event); err != nil {
				return nil, err
			}
		}
	}
	out.Message, err = a.Finish()
	if err != nil {
		return nil, err
	}
	return out, nil
}
