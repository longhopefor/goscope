// runner-demo runs two strategies through the same lifecycle, without API keys.
package main

import (
	"context"
	"fmt"

	"github.com/longhopefor/goscope/agent"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/tool"
)

type direct struct{}

func (direct) Execute(ctx context.Context, req agent.ExecutionRequest) (*agent.Result, error) {
	if err := ctx.Err(); err != nil {
		return &agent.Result{}, err
	}
	return &agent.Result{Final: msg.NewText("assistant", msg.RoleAssistant, "direct answer")}, nil
}

type scripted struct{}

func (scripted) Generate(context.Context, model.Request) (*model.Response, error) {
	return &model.Response{Message: msg.NewText("assistant", msg.RoleAssistant, "ReAct answer")}, nil
}
func main() {
	registry, err := tool.NewRegistry()
	if err != nil {
		panic(err)
	}
	react, err := agent.New(scripted{}, registry, 2)
	if err != nil {
		panic(err)
	}
	for _, item := range []struct {
		name     string
		strategy agent.Agent
	}{{"direct", direct{}}, {"react", react}} {
		runner, err := agent.NewRunner(item.strategy)
		if err != nil {
			panic(err)
		}
		result, err := runner.Run(context.Background(), agent.RunRequest{Messages: []*msg.Msg{msg.NewText("user", msg.RoleUser, "hello")}, Hook: func(e agent.Event) error { fmt.Printf("%s #%d %s\n", item.name, e.Sequence, e.Type); return nil }})
		if err != nil {
			panic(err)
		}
		fmt.Printf("%s stop=%s steps=%d answer=%s\n", item.name, result.StopReason, result.Steps, result.Final.Text())
	}
}
