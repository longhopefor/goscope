// cancel-demo uses a scripted model and real local tools; no API key is required.
package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/longhopefor/goscope/agent"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/tool"
	"os"
	"time"
)

type scripted struct{}

func (scripted) Generate(context.Context, model.Request) (*model.Response, error) {
	return &model.Response{Message: msg.New("assistant", msg.RoleAssistant, msg.ToolUse("A", "add", []byte(`{}`)), msg.ToolUse("B", "write", []byte(`{}`)), msg.ToolUse("C", "later", []byte(`{}`)))}, nil
}
func main() {
	dir, err := os.MkdirTemp("", "goscope-cancel-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	cleaned, third := false, false
	a, _ := tool.New("add", "return 42", func(context.Context, struct{}) ([]msg.Block, error) { return []msg.Block{msg.Text("42")}, nil })
	b, _ := tool.New("write", "write before cancellation", func(ctx context.Context, _ struct{}) ([]msg.Block, error) {
		if err := os.WriteFile(dir+"/receipt", []byte("written"), 0600); err != nil {
			return nil, err
		}
		defer func() { cleaned = true }()
		<-ctx.Done()
		return nil, ctx.Err()
	})
	c, _ := tool.New("later", "must not run", func(context.Context, struct{}) ([]msg.Block, error) { third = true; return nil, nil })
	registry, err := tool.NewRegistry(a, b, c)
	if err != nil {
		panic(err)
	}
	runner, err := agent.New(scripted{}, registry, 3)
	if err != nil {
		panic(err)
	}
	result, err := runner.RunWithRequest(context.Background(), agent.RunRequest{Messages: []*msg.Msg{msg.NewText("user", msg.RoleUser, "run three tools")}, Timeouts: agent.Timeouts{Run: time.Second, Model: time.Second, Tool: 30 * time.Millisecond}})
	fmt.Printf("stop=%s deadline=%v\n", result.StopReason, errors.Is(err, context.DeadlineExceeded))
	for _, call := range result.ToolBatches[0].Calls {
		fmt.Printf("%s: %s\n", call.CallID, call.Outcome)
	}
	_, statErr := os.Stat(dir + "/receipt")
	fmt.Printf("B side effect exists=%v, B cleanup=%v, C invoked=%v\n", statErr == nil, cleaned, third)
	fmt.Printf("confirmed results=%d\n", len(result.ToolBatches[0].Message.Blocks))
}
