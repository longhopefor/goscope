package main

import (
	"encoding/json"
	"fmt"
	"github.com/longhopefor/goscope/msg"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	user := msg.NewText("user", msg.RoleUser, "北京今天天气怎么样？")
	a := msg.NewAggregator("weather_assistant")
	must(a.Apply(msg.StreamEvent{Type: msg.BlockStart, BlockID: "call", Block: msg.ToolUse("call_1", "get_weather", nil)}))
	must(a.Apply(msg.StreamEvent{Type: msg.BlockDelta, BlockID: "call", Delta: `{"city":`}))
	must(a.Apply(msg.StreamEvent{Type: msg.BlockDelta, BlockID: "call", Delta: `"北京"}`}))
	must(a.Apply(msg.StreamEvent{Type: msg.BlockEnd, BlockID: "call"}))
	request, err := a.Finish()
	must(err)
	raw, err := json.MarshalIndent(request, "", "  ")
	must(err)
	fmt.Println("工具调用消息：", string(raw))
	var restored msg.Msg
	must(json.Unmarshal(raw, &restored))
	call := restored.Blocks[0].ToolUse
	result := msg.New("get_weather", msg.RoleTool, msg.ToolResult(call.ID,
		msg.Text("演示数据：晴，25℃"),
		msg.Image(msg.Source{Kind: "url", URL: "https://example.com/weather.png"}),
	))
	must(msg.ValidateConversation([]*msg.Msg{user, &restored, result}, true))
	cloned, err := result.Clone()
	must(err)
	cloned.Blocks[0].ToolResult.Output[0].Text.Text = "修改副本"
	fmt.Println("原结果：", result.Blocks[0].ToolResult.Output[0].Text.Text)
	fmt.Println("JSON 往返、流式参数、图文结果及调用关联校验通过。天气为演示数据。")
}
