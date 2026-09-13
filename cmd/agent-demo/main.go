package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/longhopefor/goscope/agent"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/model/openai"
	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/tool"
)

type args struct {
	A int64 `json:"a" tool:"required,min=-1000000,max=1000000"`
	B int64 `json:"b" tool:"required,min=-1000000,max=1000000"`
}
type script struct{}

func (script) Generate(ctx context.Context, r model.Request) (*model.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	last := r.Messages[len(r.Messages)-1]
	if last.Role == msg.RoleTool {
		if len(last.Blocks) != 1 || last.Blocks[0].ToolResult == nil {
			return nil, fmt.Errorf("unexpected tool result")
		}
		result := last.Blocks[0].ToolResult
		if result.IsError || result.ToolUseID != "call_1" || len(result.Output) != 1 || result.Output[0].Text == nil {
			return nil, fmt.Errorf("add failed")
		}
		return &model.Response{Message: msg.NewText("assistant", msg.RoleAssistant, "计算结果是 "+result.Output[0].Text.Text+"。")}, nil
	}
	return &model.Response{Message: msg.New("assistant", msg.RoleAssistant, msg.ToolUse("call_1", "add", json.RawMessage(`{"a":12,"b":30}`)))}, nil
}
func main() {
	real := flag.Bool("real", false, "使用环境变量中的真实模型服务")
	steps := flag.Int("max-steps", 4, "模型调用次数上限")
	flag.Parse()
	add, err := tool.New("add", "把两个整数相加", func(ctx context.Context, a args) ([]msg.Block, error) {
		return []msg.Block{msg.Text(fmt.Sprint(a.A + a.B))}, ctx.Err()
	})
	if err != nil {
		log.Fatal(err)
	}
	registry, err := tool.NewRegistry(add)
	if err != nil {
		log.Fatal(err)
	}
	var m model.Model = script{}
	if *real {
		m, err = openai.New(openai.Config{APIKey: os.Getenv("OPENAI_API_KEY"), Model: os.Getenv("OPENAI_MODEL"), BaseURL: os.Getenv("OPENAI_BASE_URL")})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("模式：真实模型服务")
	} else {
		fmt.Println("模式：脚本模型 + 本地真实加法函数")
	}
	a, err := agent.New(m, registry, *steps)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := a.RunWithRequest(ctx, agent.RunRequest{
		Messages: []*msg.Msg{msg.NewText("user", msg.RoleUser, "请调用 add 计算 12 + 30，然后告诉我结果。")},
		Hook: func(e agent.Event) error {
			switch e.Type {
			case agent.RunStarted:
				fmt.Println("运行开始")
			case agent.ModelStarted:
				fmt.Printf("第 %d 次模型调用开始\n", e.Step)
			case agent.ModelFinished:
				fmt.Printf("第 %d 次模型调用结束：%s\n", e.Step, e.Status)
			case agent.ToolStarted:
				fmt.Printf("工具 %s [%s] 开始\n", e.ToolName, e.ToolCallID)
			case agent.ToolFinished:
				fmt.Printf("工具 %s [%s] 结束：%s\n", e.ToolName, e.ToolCallID, e.Status)
			case agent.RunFinished:
				fmt.Printf("运行结束：%s\n", e.StopReason)
			}
			return nil
		},
	})
	fmt.Printf("停止原因：%s；模型调用次数：%d\n", result.StopReason, result.Steps)
	for _, message := range result.History {
		for _, b := range message.Blocks {
			if b.ToolResult != nil {
				fmt.Printf("工具结果：%s error=%t\n", b.ToolResult.ToolUseID, b.ToolResult.IsError)
			}
		}
	}
	if result.Final != nil {
		fmt.Println("最终回答：" + result.Final.Text())
	}
	if err != nil {
		log.Fatal(err)
	}
}
