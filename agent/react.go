// Package agent 编排完整模型响应与串行工具执行，不解析模型的内部推理。
package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"

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
	History    []*msg.Msg
	Final      *msg.Msg
	Steps      int
	StopReason StopReason
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
	r := &Result{StopReason: Failed}
	stop := func(err error) (*Result, error) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			r.StopReason = Canceled
		}
		return r, err
	}
	if a == nil || ctx == nil {
		return stop(fmt.Errorf("agent and context are required"))
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
		r.Steps++
		response, generateErr := a.model.Generate(ctx, model.Request{Messages: request, Tools: a.tools.Definitions()})
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
		result, executeErr := a.tools.Execute(ctx, assistant)
		if executeErr != nil {
			return stop(fmt.Errorf("tools: %w", executeErr))
		}
		r.History = append(r.History, result)
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
