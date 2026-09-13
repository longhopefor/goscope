package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"reflect"
)

// Registry 构造后只读，没有运行期 Register。并发安全依赖工具函数自身无共享数据竞争。
type Registry struct {
	tools       map[string]Tool
	definitions []model.Tool
}

func NewRegistry(tools ...Tool) (*Registry, error) {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("nil tool")
		}
		v := reflect.ValueOf(t)
		if (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface) && v.IsNil() {
			return nil, fmt.Errorf("nil tool")
		}
		d := t.Definition()
		if !validName.MatchString(d.Name) {
			return nil, fmt.Errorf("invalid tool name")
		}
		if _, ok := r.tools[d.Name]; ok {
			return nil, fmt.Errorf("duplicate tool %q", d.Name)
		}
		var schema map[string]any
		if json.Unmarshal(d.Parameters, &schema) != nil || schema["type"] != "object" {
			return nil, fmt.Errorf("tool schema must be object")
		}
		d.Parameters = append(json.RawMessage(nil), d.Parameters...)
		r.tools[d.Name] = t
		r.definitions = append(r.definitions, d)
	}
	return r, nil
}
func (r *Registry) Definitions() []model.Tool {
	out := make([]model.Tool, len(r.definitions))
	copy(out, r.definitions)
	for i := range out {
		out[i].Parameters = append(json.RawMessage(nil), out[i].Parameters...)
	}
	return out
}

// Execute 验证整条 assistant 消息后串行执行。普通失败变为工具结果；取消返回 error。
// 取消可能发生在已有副作用之后，返回 error 不代表之前的调用未执行，不可盲目重试整批。
func (r *Registry) Execute(ctx context.Context, request *msg.Msg) (*msg.Msg, error) {
	if r == nil || ctx == nil {
		return nil, fmt.Errorf("registry and context are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot, err := request.Clone()
	if err != nil {
		return nil, err
	}
	if snapshot.Role != msg.RoleAssistant {
		return nil, fmt.Errorf("expected assistant message")
	}
	result := msg.New("tools", msg.RoleTool)
	for _, b := range snapshot.Blocks {
		if b.Type != msg.BlockToolUse {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		call := b.ToolUse
		t, ok := r.tools[call.Name]
		var output []msg.Block
		var callErr error
		if !ok {
			callErr = failure("unknown_tool", "tool is not registered")
		} else {
			output, callErr = invoke(ctx, t, call.Input)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if cancellation(callErr) {
			return nil, callErr
		}
		block := msg.ToolResult(call.ID, output...)
		if callErr != nil {
			code := "execution_failed"
			message := "tool execution failed"
			// 只透传包装器的可控错误，不将任意业务错误/堆栈泄露给模型。
			if e, ok := callErr.(*Error); ok {
				code = e.Code
				message = e.Message
			}
			raw, _ := json.Marshal(map[string]string{"code": code, "message": message})
			block = msg.ToolResult(call.ID, msg.Text(string(raw)))
			block.ToolResult.IsError = true
		}
		result.Add(block)
	}
	if len(result.Blocks) == 0 {
		return nil, fmt.Errorf("no tool calls")
	}
	return result, result.Validate()
}
func invoke(ctx context.Context, t Tool, raw json.RawMessage) (out []msg.Block, err error) {
	defer func() {
		if recover() != nil {
			out = nil
			err = failure("panic", "tool panicked")
		}
	}()
	out, err = t.Call(ctx, raw)
	if err != nil {
		return nil, err
	}
	return cloneOutput(out)
}
