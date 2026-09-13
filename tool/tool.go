// Package tool 将明确类型的 Go 函数包装成可描述、可校验、可调用的工具。
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"reflect"
	"regexp"
)

type Tool interface {
	Definition() model.Tool
	Call(context.Context, json.RawMessage) ([]msg.Block, error)
}
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string           { return e.Code + ": " + e.Message }
func failure(code, message string) error { return &Error{Code: code, Message: message} }

var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type typed[T any] struct {
	definition model.Tool
	shape      *node
	fn         func(context.Context, T) ([]msg.Block, error)
}

// New 在运行时构建受限 JSON Schema。T 必须为结构体，标签规则见 README。
func New[T any](name, description string, fn func(context.Context, T) ([]msg.Block, error)) (Tool, error) {
	if !validName.MatchString(name) || fn == nil {
		return nil, fmt.Errorf("invalid tool name or nil function")
	}
	t := reflect.TypeOf((*T)(nil)).Elem()
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("tool arguments must be struct")
	}
	shape, err := describe(t, map[reflect.Type]bool{})
	if err != nil {
		return nil, err
	}
	schema, err := json.Marshal(shape.schema())
	if err != nil {
		return nil, err
	}
	return &typed[T]{definition: model.Tool{Name: name, Description: description, Parameters: schema}, shape: shape, fn: fn}, nil
}
func (t *typed[T]) Definition() model.Tool {
	d := t.definition
	d.Parameters = append(json.RawMessage(nil), d.Parameters...)
	return d
}
func (t *typed[T]) Call(ctx context.Context, raw json.RawMessage) (out []msg.Block, err error) {
	if ctx == nil {
		return nil, fmt.Errorf("nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	defer func() {
		if recover() != nil {
			out = nil
			err = failure("panic", "tool panicked")
		}
	}()
	value, err := parse(raw)
	if err != nil {
		return nil, failure("invalid_arguments", err.Error())
	}
	if err := t.shape.validate(value, "args"); err != nil {
		return nil, failure("invalid_arguments", err.Error())
	}
	var args T
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, failure("invalid_arguments", "cannot decode arguments")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out, err = t.fn(ctx, args)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return cloneOutput(out)
}
func cloneOutput(out []msg.Block) ([]msg.Block, error) {
	m := msg.New("tool", msg.RoleTool, msg.ToolResult("copy", out...))
	c, err := m.Clone()
	if err != nil {
		return nil, failure("invalid_output", "tool returned unsupported or invalid content")
	}
	return c.Blocks[0].ToolResult.Output, nil
}
func cancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
