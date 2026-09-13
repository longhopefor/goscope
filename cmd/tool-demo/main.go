package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/tool"
	"log"
)

type AddArgs struct {
	A int64 `json:"a" tool:"required,min=-1000000,max=1000000" description:"第一个整数"`
	B int64 `json:"b" tool:"required,min=-1000000,max=1000000" description:"第二个整数"`
}

func main() {
	add, err := tool.New("add", "把两个整数相加", func(ctx context.Context, a AddArgs) ([]msg.Block, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return []msg.Block{msg.Text(fmt.Sprint(a.A + a.B))}, nil
	})
	if err != nil {
		log.Fatal(err)
	}
	registry, err := tool.NewRegistry(add)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("工具：", registry.Definitions()[0].Name)
	schema, err := json.MarshalIndent(json.RawMessage(registry.Definitions()[0].Parameters), "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("参数 Schema：", string(schema))
	request := msg.New("assistant", msg.RoleAssistant,
		msg.ToolUse("call_1", "add", json.RawMessage(`{"a":12,"b":30}`)),
		msg.ToolUse("call_2", "add", json.RawMessage(`{"a":0}`)),
		msg.ToolUse("call_3", "missing", json.RawMessage(`{}`)),
	)
	result, err := registry.Execute(context.Background(), request)
	if err != nil {
		log.Fatal(err)
	}
	if err := msg.ValidateConversation([]*msg.Msg{request, result}, true); err != nil {
		log.Fatal(err)
	}
	for _, b := range result.Blocks {
		r := b.ToolResult
		fmt.Printf("%s error=%t output=%s\n", r.ToolUseID, r.IsError, r.Output[0].Text.Text)
	}
	fmt.Println("工具函数已在本机执行；调用消息为手动构造，未调用模型。")
}
