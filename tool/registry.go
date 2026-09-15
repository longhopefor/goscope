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
	return r.ExecuteWithObserver(ctx, request, nil)
}

// ExecuteWithObserver 发出每项调用的值类型进度，观察者不能修改调用内容。
func (r *Registry) ExecuteWithObserver(ctx context.Context, request *msg.Msg, observer func(Progress)) (*msg.Msg, error) {
	batch, err := r.ExecuteBatch(ctx, request, BatchOptions{Observer: observer})
	if err != nil {
		return nil, err
	}
	return batch.Message, nil
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
