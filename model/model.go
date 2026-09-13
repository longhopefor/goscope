// Package model 定义厂商无关的模型调用边界，不执行工具。
package model

import (
	"context"
	"encoding/json"
	"github.com/longhopefor/goscope/msg"
)

type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}
type Request struct {
	Messages []*msg.Msg
	Tools    []Tool
}
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
type Response struct {
	Message      *msg.Msg
	ID           string
	Model        string
	FinishReason string
	Usage        *Usage
}
type Model interface {
	Generate(context.Context, Request) (*Response, error)
}

// Streamer 同步调用 emit；回调返回错误立即停止。已发出的事件为暂态，
// 只有返回 err==nil 才能把 Response 视作完成消息。回调不执行工具。
type Streamer interface {
	Stream(context.Context, Request, func(msg.StreamEvent) error) (*Response, error)
}

// Formatter 只处理数据映射，不能发 HTTP 请求。每个协议提供自己的实现。
type Formatter interface {
	Encode(Request, string, bool) ([]byte, error)
	Decode([]byte) (*Response, error)
}
