// stream-demo streams two model rounds around a real local tool. No API key.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/longhopefor/goscope/agent"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/tool"
)

type scripted struct{}

func (scripted) Generate(context.Context, model.Request) (*model.Response, error) {
	return nil, fmt.Errorf("use Stream")
}
func (scripted) Stream(ctx context.Context, req model.Request, emit func(msg.StreamEvent) error) (*model.Response, error) {
	var events []msg.StreamEvent
	if len(req.Messages) == 1 {
		events = []msg.StreamEvent{
			{Type: msg.BlockStart, BlockID: "text", Block: msg.Text("")},
			{Type: msg.BlockDelta, BlockID: "text", Delta: "Let me calculate."},
			{Type: msg.BlockEnd, BlockID: "text"},
			{Type: msg.BlockStart, BlockID: "call", Block: msg.ToolUse("A", "add", nil)},
			{Type: msg.BlockDelta, BlockID: "call", Delta: `{"a":20,`},
			{Type: msg.BlockDelta, BlockID: "call", Delta: `"b":22}`},
			{Type: msg.BlockEnd, BlockID: "call"},
		}
	} else {
		result := req.Messages[len(req.Messages)-1].Blocks[0].ToolResult.Output[0].Text.Text
		events = []msg.StreamEvent{{Type: msg.BlockStart, BlockID: "text", Block: msg.Text("")}, {Type: msg.BlockDelta, BlockID: "text", Delta: "Answer: "}, {Type: msg.BlockDelta, BlockID: "text", Delta: result}, {Type: msg.BlockEnd, BlockID: "text"}}
	}
	a := msg.NewAggregator("assistant")
	for _, e := range events {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := a.Apply(e); err != nil {
			return nil, err
		}
		if err := emit(e); err != nil {
			return nil, err
		}
	}
	message, err := a.Finish()
	return &model.Response{Message: message}, err
}

type args struct {
	A int `json:"a"`
	B int `json:"b"`
}

func main() {
	add, err := tool.New("add", "add integers", func(_ context.Context, a args) ([]msg.Block, error) {
		return []msg.Block{msg.Text(fmt.Sprint(a.A + a.B))}, nil
	})
	if err != nil {
		panic(err)
	}
	reg, err := tool.NewRegistry(add)
	if err != nil {
		panic(err)
	}
	react, err := agent.New(scripted{}, reg, 3)
	if err != nil {
		panic(err)
	}
	runner, err := agent.NewRunner(react)
	if err != nil {
		panic(err)
	}
	stream, err := runner.Stream(context.Background(), agent.RunRequest{Messages: []*msg.Msg{msg.NewText("user", msg.RoleUser, "20+22?")}})
	if err != nil {
		panic(err)
	}
	defer stream.Close()
	for {
		e, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			panic(err)
		}
		if e.Event.Type == msg.BlockDelta {
			fmt.Printf("step=%d block=%s delta=%q\n", e.Step, e.Event.BlockID, e.Event.Delta)
		}
	}
	result, err := stream.Wait()
	if err != nil {
		panic(err)
	}
	fmt.Printf("stop=%s steps=%d tools=%d final=%q\n", result.StopReason, result.Steps, len(result.ToolBatches[0].Calls), result.Final.Text())
}
