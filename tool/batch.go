package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/longhopefor/goscope/msg"
	"time"
)

type Outcome string

const (
	NotStarted Outcome = "not_started"
	Succeeded  Outcome = "succeeded"
	Failed     Outcome = "failed"
	Unknown    Outcome = "unknown"
)

// CallResult records the Tool.Call boundary, not a durable business transaction.
// Failed means a tool error was recorded; it does not imply no side effects.
type CallResult struct {
	CallID    string
	Name      string
	AttemptID string
	Outcome   Outcome
}

// BatchResult retains confirmed results on cancellation. Message may be nil.
// Unknown and unstarted calls have no fabricated tool-result messages.
type BatchResult struct {
	Calls   []CallResult
	Message *msg.Msg
}
type BatchOptions struct {
	Timeout  time.Duration // per invocation; zero inherits the parent deadline
	Observer func(Progress)
}

func (r *Registry) ExecuteBatch(ctx context.Context, request *msg.Msg, opts BatchOptions) (*BatchResult, error) {
	batch := &BatchResult{}
	if r == nil || ctx == nil || opts.Timeout < 0 {
		return batch, fmt.Errorf("registry, context and nonnegative timeout are required")
	}
	snapshot, err := request.Clone()
	if err != nil {
		return batch, err
	}
	if snapshot.Role != msg.RoleAssistant {
		return batch, fmt.Errorf("expected assistant message")
	}
	var calls []*msg.ToolUseBlock
	for _, b := range snapshot.Blocks {
		if b.Type == msg.BlockToolUse {
			calls = append(calls, b.ToolUse)
			batch.Calls = append(batch.Calls, CallResult{CallID: b.ToolUse.ID, Name: b.ToolUse.Name, Outcome: NotStarted})
		}
	}
	if len(calls) == 0 {
		return batch, fmt.Errorf("no tool calls")
	}
	for i, call := range calls {
		if err := ctx.Err(); err != nil {
			return batch, err
		}
		item := &batch.Calls[i]
		report := func(finished bool, status string) {
			reportProgress(opts.Observer, Progress{CallID: call.ID, Name: call.Name, Finished: finished, Status: status})
		}
		report(false, "started")
		if err := ctx.Err(); err != nil {
			report(true, "canceled")
			return batch, err
		}
		var output []msg.Block
		var callErr error
		if t, ok := r.tools[call.Name]; ok {
			item.AttemptID = msg.NewID()
			output, callErr = invokeTimed(ctx, t, call.Input, opts.Timeout)
		} else {
			callErr = failure("unknown_tool", "tool is not registered")
		}
		if cancellation(callErr) {
			item.Outcome = Unknown
			report(true, "canceled")
			return batch, callErr
		}
		block := msg.ToolResult(call.ID, output...)
		item.Outcome = Succeeded
		if callErr != nil {
			item.Outcome = Failed
			code, message := "execution_failed", "tool execution failed"
			if e, ok := callErr.(*Error); ok {
				code, message = e.Code, e.Message
			}
			raw, _ := json.Marshal(map[string]string{"code": code, "message": message})
			block = msg.ToolResult(call.ID, msg.Text(string(raw)))
			block.ToolResult.IsError = true
		}
		if batch.Message == nil {
			batch.Message = msg.New("tools", msg.RoleTool)
		}
		batch.Message.Add(block)
		report(true, string(item.Outcome))
		if err := ctx.Err(); err != nil {
			return batch, err
		}
	}
	return batch, batch.Message.Validate()
}

func invokeTimed(ctx context.Context, t Tool, raw json.RawMessage, timeout time.Duration) ([]msg.Block, error) {
	var child context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		child, cancel = context.WithTimeout(ctx, timeout)
	} else {
		child, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	output, err := invoke(child, t, raw)
	if child.Err() != nil {
		return nil, child.Err()
	}
	return output, err
}
