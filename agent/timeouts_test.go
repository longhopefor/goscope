package agent

import (
	"context"
	"errors"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/tool"
	"testing"
	"time"
)

type timeoutModel func(context.Context, model.Request) (*model.Response, error)

func (f timeoutModel) Generate(c context.Context, r model.Request) (*model.Response, error) {
	return f(c, r)
}
func TestRunAndModelTimeout(t *testing.T) {
	for _, timeouts := range []Timeouts{{Run: 10 * time.Millisecond}, {Model: 10 * time.Millisecond}} {
		cleaned := false
		m := timeoutModel(func(ctx context.Context, _ model.Request) (*model.Response, error) {
			defer func() { cleaned = true }()
			<-ctx.Done()
			return nil, ctx.Err()
		})
		registry, _ := tool.NewRegistry()
		a, _ := New(m, registry, 2)
		r, err := a.RunWithRequest(context.Background(), RunRequest{Messages: []*msg.Msg{msg.NewText("user", msg.RoleUser, "hi")}, Timeouts: timeouts})
		if !errors.Is(err, context.DeadlineExceeded) || r.StopReason != Canceled || !cleaned {
			t.Fatalf("%+v %v", r, err)
		}
	}
}
func TestRunPreservesPartialToolHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	aTool, _ := tool.New("a", "a", func(context.Context, struct{}) ([]msg.Block, error) { return []msg.Block{msg.Text("done")}, nil })
	bTool, _ := tool.New("b", "b", func(context.Context, struct{}) ([]msg.Block, error) { cancel(); return nil, context.Canceled })
	registry, _ := tool.NewRegistry(aTool, bTool)
	var saved context.Context
	m := timeoutModel(func(ctx context.Context, _ model.Request) (*model.Response, error) {
		saved = ctx
		return &model.Response{Message: msg.New("assistant", msg.RoleAssistant, msg.ToolUse("a", "a", []byte(`{}`)), msg.ToolUse("b", "b", []byte(`{}`)))}, nil
	})
	a, _ := New(m, registry, 2)
	r, err := a.Run(ctx, []*msg.Msg{msg.NewText("user", msg.RoleUser, "hi")})
	if !errors.Is(err, context.Canceled) || len(r.History) != 3 || len(r.ToolBatches) != 1 || saved.Err() == nil {
		t.Fatalf("%+v %v", r, err)
	}
	if msg.ValidateConversation(r.History, false) != nil || msg.ValidateConversation(r.History, true) == nil {
		t.Fatal("partial history must remain unresolved")
	}
	r.ToolBatches[0].Message.Blocks[0].ToolResult.Output[0].Text.Text = "mutated"
	if r.History[2].Blocks[0].ToolResult.Output[0].Text.Text != "done" {
		t.Fatal("history aliases batch")
	}
}

func TestModelSuccessReleasesContext(t *testing.T) {
	var child context.Context
	registry, _ := tool.NewRegistry()
	a, _ := New(timeoutModel(func(ctx context.Context, _ model.Request) (*model.Response, error) {
		child = ctx
		return &model.Response{Message: msg.NewText("assistant", msg.RoleAssistant, "done")}, nil
	}), registry, 1)
	_, err := a.Run(context.Background(), []*msg.Msg{msg.NewText("user", msg.RoleUser, "hi")})
	if err != nil || child.Err() == nil {
		t.Fatalf("child was not released: %v", err)
	}
}
func TestParentDeadlineWins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	registry, _ := tool.NewRegistry()
	a, _ := New(timeoutModel(func(child context.Context, _ model.Request) (*model.Response, error) {
		got, _ := child.Deadline()
		if !got.Equal(deadline) {
			t.Fatal("extended parent deadline")
		}
		return &model.Response{Message: msg.NewText("assistant", msg.RoleAssistant, "done")}, nil
	}), registry, 1)
	_, err := a.RunWithRequest(ctx, RunRequest{Messages: []*msg.Msg{msg.NewText("user", msg.RoleUser, "hi")}, Timeouts: Timeouts{Run: time.Hour, Model: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
}
