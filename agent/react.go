// Package agent 编排完整模型响应与串行工具执行，不解析模型的内部推理。
package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/tool"
)

type StopReason string

const (
	Completed StopReason = "completed"
	MaxSteps  StopReason = "max_steps"
	Canceled  StopReason = "canceled"
	Failed    StopReason = "failed"
)

var ErrMaxSteps = errors.New("agent reached maximum model steps")

// Result 即使失败也返回；History 是诊断快照，取消时可能有未解决调用。
// Steps 计算已启动的 Generate 次数，Final 仅在正常完成时赋值。
type Result struct {
	History      []*msg.Msg
	Final        *msg.Msg
	Steps        int
	StopReason   StopReason
	RunID        string
	HookFailures int
	ToolBatches  []*tool.BatchResult
}

type ReAct struct {
	model    model.Model
	tools    *tool.Registry
	maxSteps int
}

func New(m model.Model, tools *tool.Registry, maxSteps int) (*ReAct, error) {
	if m == nil || tools == nil || maxSteps <= 0 {
		return nil, fmt.Errorf("model, registry and positive maxSteps are required")
	}
	v := reflect.ValueOf(m)
	switch v.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Func, reflect.Slice, reflect.Interface, reflect.Chan:
		if v.IsNil() {
			return nil, fmt.Errorf("nil model")
		}
	}
	return &ReAct{model: m, tools: tools, maxSteps: maxSteps}, nil
}
func clone(messages []*msg.Msg) ([]*msg.Msg, error) {
	out := make([]*msg.Msg, len(messages))
	for i, m := range messages {
		c, err := m.Clone()
		if err != nil {
			return nil, err
		}
		out[i] = c
	}
	return out, nil
}

// Run 使用独立历史。模型请求与返回值均复制；调用方不能并发修改传入消息。
// 工具取消不回滚副作用，也不自动重试。依赖对象自身须支持并发，才可并发 Run。
func (a *ReAct) Run(ctx context.Context, input []*msg.Msg) (*Result, error) {
	return a.RunWithRequest(ctx, RunRequest{Messages: input})
}

// RunWithRequest 为本次运行设置进度 Hook；原 Run 接口继续可用。
func (a *ReAct) RunWithRequest(ctx context.Context, req RunRequest) (*Result, error) {
	started := time.Now()
	input := req.Messages
	r := &Result{StopReason: Failed, RunID: msg.NewID()}
	sequence := 0
	emit := func(e Event) {
		sequence++
		e.RunID, e.Sequence, e.Time = r.RunID, sequence, time.Now()
		if e.Step == 0 {
			e.Step = r.Steps
		}
		if notify(req.Hook, e) != nil {
			r.HookFailures++
		}
	}
	emit(Event{Type: RunStarted, Status: "started"})
	defer func() { emit(Event{Type: RunFinished, StopReason: r.StopReason}) }()
	modelActive := false
	modelStep := 0
	stop := func(err error) (*Result, error) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			r.StopReason = Canceled
		}
		if modelActive {
			status := "failed"
			if r.StopReason == Canceled {
				status = "canceled"
			}
			emit(Event{Type: ModelFinished, Status: status, Step: modelStep})
			modelActive = false
		}
		return r, err
	}
	if a == nil || ctx == nil || a.model == nil || a.tools == nil || a.maxSteps <= 0 {
		return stop(fmt.Errorf("agent and context are required"))
	}
	if req.Timeouts.Run < 0 || req.Timeouts.Model < 0 || req.Timeouts.Tool < 0 {
		return stop(fmt.Errorf("timeouts must be nonnegative"))
	}
	if req.Timeouts.Run > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, started.Add(req.Timeouts.Run))
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return stop(err)
	}
	if len(input) == 0 {
		return stop(fmt.Errorf("empty history"))
	}
	history, err := clone(input)
	if err != nil {
		return stop(err)
	}
	if err = msg.ValidateConversation(history, true); err != nil {
		return stop(err)
	}
	r.History = history
	for r.Steps < a.maxSteps {
		if err = ctx.Err(); err != nil {
			return stop(err)
		}
		request, copyErr := clone(r.History)
		if copyErr != nil {
			return stop(copyErr)
		}
		emitStep := r.Steps + 1
		// 在启动通知之后再次检查取消；Step 标记本次尝试，Steps 只计实际调用。
		emit(Event{Type: ModelStarted, Status: "started", Step: emitStep})
		modelActive = true
		modelStep = emitStep
		if err = ctx.Err(); err != nil {
			return stop(err)
		}
		r.Steps++
		response, generateErr := a.generate(ctx, model.Request{Messages: request, Tools: a.tools.Definitions()}, req.Timeouts.Model)
		if err = ctx.Err(); err != nil {
			return stop(err)
		}
		if generateErr != nil {
			return stop(fmt.Errorf("model: %w", generateErr))
		}
		if response == nil || response.Message == nil {
			return stop(fmt.Errorf("model returned no message"))
		}
		assistant, copyErr := response.Message.Clone()
		if copyErr != nil {
			return stop(copyErr)
		}
		if assistant.Role != msg.RoleAssistant {
			return stop(fmt.Errorf("model must return assistant message"))
		}
		candidate := append(r.History, assistant)
		if err = msg.ValidateConversation(candidate, false); err != nil {
			return stop(err)
		}
		r.History = candidate
		emit(Event{Type: ModelFinished, Status: "succeeded"})
		modelActive = false
		hasTools := len(assistant.BlocksOfType(msg.BlockToolUse)) > 0
		if !hasTools {
			if err = ctx.Err(); err != nil {
				return stop(err)
			}
			r.Final, err = assistant.Clone()
			if err != nil {
				return stop(err)
			}
			r.StopReason = Completed
			return r, nil
		}
		if err = ctx.Err(); err != nil {
			return stop(err)
		}
		result, executeErr := a.tools.ExecuteBatch(ctx, assistant, tool.BatchOptions{Timeout: req.Timeouts.Tool, Observer: func(p tool.Progress) {
			eventType := ToolStarted
			if p.Finished {
				eventType = ToolFinished
			}
			emit(Event{Type: eventType, ToolCallID: p.CallID, ToolName: p.Name, Status: p.Status})
		}})
		r.ToolBatches = append(r.ToolBatches, result)
		if result.Message != nil {
			confirmed, copyErr := result.Message.Clone()
			if copyErr != nil {
				return stop(copyErr)
			}
			r.History = append(r.History, confirmed)
		}
		if executeErr != nil {
			return stop(fmt.Errorf("tools: %w", executeErr))
		}
		if err = msg.ValidateConversation(r.History, true); err != nil {
			return stop(err)
		}
		if err = ctx.Err(); err != nil {
			return stop(err)
		}
	}
	r.StopReason = MaxSteps
	return r, ErrMaxSteps
}

func (a *ReAct) generate(ctx context.Context, req model.Request, timeout time.Duration) (*model.Response, error) {
	var child context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		child, cancel = context.WithTimeout(ctx, timeout)
	} else {
		child, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	if err := child.Err(); err != nil {
		return nil, err
	}
	response, err := a.model.Generate(child, req)
	if child.Err() != nil {
		return nil, child.Err()
	}
	return response, err
}
