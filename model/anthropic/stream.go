package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"strconv"
	"strings"

	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
)

type event struct {
	Type    string    `json:"type"`
	Message *envelope `json:"message"`
	Index   *int      `json:"index"`
	Block   *block    `json:"content_block"`
	Delta   struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage map[string]json.RawMessage `json:"usage"`
}

func (c *Client) Stream(ctx context.Context, r model.Request, emit func(msg.StreamEvent) error) (*model.Response, error) {
	res, err := c.send(ctx, r, true)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	media, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || media != "text/event-stream" {
		return nil, fmt.Errorf("expected text/event-stream")
	}
	aggregate := msg.NewAggregator("assistant")
	apply := func(e msg.StreamEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := aggregate.Apply(e); err != nil {
			return err
		}
		if emit != nil {
			return emit(e)
		}
		return nil
	}
	var response *envelope
	next, active, calls := 0, -1, 0
	kind := ""
	hasJSON := false
	reasonSeen := false
	deltaSeen := false
	err = readSSE(res.Body, func(name, data string) (bool, error) {
		var e event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return false, fmt.Errorf("invalid stream JSON: %w", err)
		}
		if name != "" && name != e.Type {
			return false, fmt.Errorf("SSE event name/type mismatch")
		}
		if e.Type == "error" {
			return false, fmt.Errorf("provider returned stream error")
		}
		if e.Type == "ping" {
			return false, nil
		}
		switch e.Type {
		case "message_start":
			if response != nil || e.Message == nil || e.Message.Type != "message" || e.Message.Role != "assistant" || e.Message.ID == "" || len(e.Message.Content) != 0 || e.Message.StopReason != "" {
				return false, fmt.Errorf("invalid message_start")
			}
			response = e.Message
		case "content_block_start":
			if response == nil || deltaSeen || active != -1 || e.Index == nil || *e.Index != next || e.Block == nil {
				return false, fmt.Errorf("invalid content block start")
			}
			active = *e.Index
			next++
			kind = e.Block.Type
			hasJSON = false
			b, err := decodeBlock(*e.Block)
			if err != nil {
				return false, err
			}
			if kind == "tool_use" {
				var initial map[string]any
				if json.Unmarshal(b.ToolUse.Input, &initial) != nil || initial == nil || len(initial) != 0 {
					return false, fmt.Errorf("tool stream must start with empty input object")
				}
				b.ToolUse.Input = nil
				calls++
			}
			if err := apply(msg.StreamEvent{Type: msg.BlockStart, BlockID: strconv.Itoa(active), Block: b}); err != nil {
				return false, err
			}
		case "content_block_delta":
			if response == nil || deltaSeen || active < 0 || e.Index == nil || *e.Index != active {
				return false, fmt.Errorf("invalid content delta")
			}
			var delta string
			switch {
			case kind == "text" && e.Delta.Type == "text_delta":
				delta = e.Delta.Text
			case kind == "tool_use" && e.Delta.Type == "input_json_delta":
				delta = e.Delta.PartialJSON
				if delta != "" {
					hasJSON = true
				}
			default:
				return false, fmt.Errorf("unsupported or mismatched delta type")
			}
			if err := apply(msg.StreamEvent{Type: msg.BlockDelta, BlockID: strconv.Itoa(active), Delta: delta}); err != nil {
				return false, err
			}
		case "content_block_stop":
			if response == nil || deltaSeen || active < 0 || e.Index == nil || *e.Index != active {
				return false, fmt.Errorf("invalid content stop")
			}
			if kind == "tool_use" && !hasJSON {
				if err := apply(msg.StreamEvent{Type: msg.BlockDelta, BlockID: strconv.Itoa(active), Delta: "{}"}); err != nil {
					return false, err
				}
			}
			if err := apply(msg.StreamEvent{Type: msg.BlockEnd, BlockID: strconv.Itoa(active)}); err != nil {
				return false, err
			}
			active = -1
		case "message_delta":
			if response == nil || active != -1 {
				return false, fmt.Errorf("invalid message delta")
			}
			deltaSeen = true
			if e.Delta.StopReason != "" {
				if reasonSeen && response.StopReason != e.Delta.StopReason {
					return false, fmt.Errorf("stop reason changed")
				}
				if err := finish(e.Delta.StopReason, calls); err != nil {
					return false, err
				}
				reasonSeen = true
				response.StopReason = e.Delta.StopReason
			}
			for key, raw := range e.Usage {
				var dest *int
				switch key {
				case "input_tokens":
					dest = &response.Usage.Input
				case "output_tokens":
					dest = &response.Usage.Output
				case "cache_read_input_tokens":
					dest = &response.Usage.CacheRead
				case "cache_creation_input_tokens":
					dest = &response.Usage.CacheCreate
				default:
					continue
				}
				if string(raw) == "null" {
					continue
				}
				var value int
				if json.Unmarshal(raw, &value) != nil || value < 0 {
					return false, fmt.Errorf("invalid token usage")
				}
				*dest = value
			}
		case "message_stop":
			if response == nil || !reasonSeen || active != -1 {
				return false, fmt.Errorf("premature message_stop")
			}
			return true, nil
		default:
			return false, fmt.Errorf("unsupported stream event %q", e.Type)
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m, err := aggregate.Finish()
	if err != nil {
		return nil, err
	}
	u, err := response.Usage.convert()
	if err != nil {
		return nil, err
	}
	return &model.Response{Message: m, ID: response.ID, Model: response.Model, FinishReason: response.StopReason, Usage: u}, nil
}

// Explicit message_stop is required. EOF alone never commits a response.
func readSSE(r io.Reader, consume func(string, string) (bool, error)) error {
	scanner := bufio.NewScanner(io.LimitReader(r, maxBody+1))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	name := ""
	var data []string
	total := 0
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > maxBody {
			return fmt.Errorf("stream exceeds size limit")
		}
		if line == "" {
			if len(data) > 0 {
				done, err := consume(name, strings.Join(data, "\n"))
				if err != nil {
					return err
				}
				if done {
					return nil
				}
			}
			name = ""
			data = nil
			continue
		}
		if strings.HasPrefix(line, "event:") {
			name = strings.TrimPrefix(strings.TrimPrefix(line, "event:"), " ")
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("stream ended without message_stop: %w", io.ErrUnexpectedEOF)
}
