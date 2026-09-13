package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/tool"
)

type modelFunc func(context.Context, model.Request) (*model.Response, error)

func (f modelFunc) Generate(c context.Context, r model.Request) (*model.Response, error) {
	return f(c, r)
}
func input() []*msg.Msg { return []*msg.Msg{msg.NewText("user", msg.RoleUser, "calculate")} }
func response(blocks ...msg.Block) *model.Response {
	return &model.Response{Message: msg.New("assistant", msg.RoleAssistant, blocks...)}
}
func registry(t *testing.T, fn func(context.Context) ([]msg.Block, error)) *tool.Registry {
	t.Helper()
	x, err := tool.New("work", "work", func(c context.Context, _ struct{}) ([]msg.Block, error) { return fn(c) })
	if err != nil {
		t.Fatal(err)
	}
	r, err := tool.NewRegistry(x)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestLoopAndIsolation(t *testing.T) {
	count := 0
	reg := registry(t, func(context.Context) ([]msg.Block, error) { count++; return []msg.Block{msg.Text("42")}, nil })
	calls := 0
	m := modelFunc(func(_ context.Context, r model.Request) (*model.Response, error) {
		calls++
		if len(r.Tools) != 1 {
			t.Fatal("definitions missing")
		}
		if calls == 1 {
			r.Messages[0].Blocks[0].Text.Text = "mutated"
			return response(msg.ToolUse("1", "work", json.RawMessage(`{}`)), msg.ToolUse("2", "missing", json.RawMessage(`{}`))), nil
		}
		if len(r.Messages) != 3 || r.Messages[0].Text() != "calculate" {
			t.Fatal("history corrupted")
		}
		results := r.Messages[2].Blocks
		if results[0].ToolResult.Output[0].Text.Text != "42" || !results[1].ToolResult.IsError {
			t.Fatal("results missing")
		}
		return response(msg.Text("done")), nil
	})
	a, _ := New(m, reg, 3)
	in := input()
	r, err := a.Run(context.Background(), in)
	if err != nil || r.StopReason != Completed || r.Steps != 2 || count != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if err := msg.ValidateConversation(r.History, true); err != nil {
		t.Fatal(err)
	}
	r.Final.Blocks[0].Text.Text = "changed"
	if r.History[3].Text() != "done" || in[0].Text() != "calculate" {
		t.Fatal("alias")
	}
}
func TestLimit(t *testing.T) {
	count := 0
	reg := registry(t, func(context.Context) ([]msg.Block, error) { count++; return []msg.Block{msg.Text("ok")}, nil })
	n := 0
	m := modelFunc(func(context.Context, model.Request) (*model.Response, error) {
		n++
		return response(msg.ToolUse(fmt.Sprint(n), "work", json.RawMessage(`{}`))), nil
	})
	a, _ := New(m, reg, 2)
	r, err := a.Run(context.Background(), input())
	if !errors.Is(err, ErrMaxSteps) || r.StopReason != MaxSteps || r.Steps != 2 || count != 2 || r.Final != nil {
		t.Fatalf("%+v %v", r, err)
	}
	if err := msg.ValidateConversation(r.History, true); err != nil {
		t.Fatal(err)
	}
}
func TestRejectModelFailuresBeforeTools(t *testing.T) {
	boom := errors.New("model failed")
	for _, tc := range []struct {
		name  string
		reply *model.Response
		err   error
	}{
		{"error", response(msg.ToolUse("1", "work", json.RawMessage(`{}`))), boom},
		{"nil", nil, nil},
		{"wrong_role", &model.Response{Message: msg.NewText("user", msg.RoleUser, "bad")}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			reg := registry(t, func(context.Context) ([]msg.Block, error) { count++; return nil, nil })
			a, _ := New(modelFunc(func(context.Context, model.Request) (*model.Response, error) { return tc.reply, tc.err }), reg, 2)
			r, err := a.Run(context.Background(), input())
			if err == nil || r.StopReason != Failed || count != 0 || r.Steps != 1 {
				t.Fatalf("%+v %v", r, err)
			}
			if tc.err != nil && !errors.Is(err, boom) {
				t.Fatal("lost cause")
			}
		})
	}
}
func TestCancellation(t *testing.T) {
	for _, where := range []string{"before", "model", "tool"} {
		t.Run(where, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tools, models := 0, 0
			reg := registry(t, func(context.Context) ([]msg.Block, error) {
				tools++
				cancel()
				return []msg.Block{msg.Text("side effect happened")}, nil
			})
			m := modelFunc(func(context.Context, model.Request) (*model.Response, error) {
				models++
				if where == "model" {
					cancel()
				}
				return response(msg.ToolUse("1", "work", json.RawMessage(`{}`)), msg.ToolUse("2", "work", json.RawMessage(`{}`))), nil
			})
			a, _ := New(m, reg, 3)
			if where == "before" {
				cancel()
			}
			r, err := a.Run(ctx, input())
			if !errors.Is(err, context.Canceled) || r.StopReason != Canceled {
				t.Fatalf("%+v %v", r, err)
			}
			if where == "before" && models != 0 || where == "model" && tools != 0 || where == "tool" && (tools != 1 || models != 1) {
				t.Fatal("continued after cancellation")
			}
		})
	}
}
func TestDuplicateCallAndUnresolvedInput(t *testing.T) {
	executed := 0
	reg := registry(t, func(context.Context) ([]msg.Block, error) { executed++; return []msg.Block{msg.Text("ok")}, nil })
	calls := 0
	m := modelFunc(func(context.Context, model.Request) (*model.Response, error) {
		calls++
		return response(msg.ToolUse("same", "work", json.RawMessage(`{}`))), nil
	})
	a, _ := New(m, reg, 3)
	r, err := a.Run(context.Background(), input())
	if err == nil || executed != 1 || r.Steps != 2 {
		t.Fatal("duplicate call executed")
	}
	_, err = a.Run(context.Background(), []*msg.Msg{response(msg.ToolUse("pending", "work", json.RawMessage(`{}`))).Message})
	if err == nil || calls != 2 {
		t.Fatal("unresolved input accepted")
	}
}
