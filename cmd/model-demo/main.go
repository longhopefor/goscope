// 默认使用本地 HTTP 模拟服务验证调用链。-live 才会访问真实模型。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/model/openai"
	"github.com/longhopefor/goscope/msg"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"time"
)

func main() {
	live := flag.Bool("live", false, "使用环境变量配置的真实模型")
	stream := flag.Bool("stream", false, "流式输出")
	flag.Parse()
	cfg := openai.Config{APIKey: os.Getenv("OPENAI_API_KEY"), Model: os.Getenv("OPENAI_MODEL"), BaseURL: os.Getenv("OPENAI_BASE_URL")}
	if !*live {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Stream bool `json:"stream"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad JSON", 400)
				return
			}
			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, part := range []string{"这是本地模拟回复。", "尚未调用真实模型。"} {
					raw, _ := json.Marshal(map[string]any{"id": "demo", "model": "mock", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"role": "assistant", "content": part}, "finish_reason": nil}}})
					fmt.Fprintf(w, "data: %s\n\n", raw)
					w.(http.Flusher).Flush()
				}
				fmt.Fprint(w, "data: {\"id\":\"demo\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			} else {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"demo","model":"mock","choices":[{"index":0,"message":{"role":"assistant","content":"这是本地模拟回复，尚未调用真实模型。"},"finish_reason":"stop"}]}`)
			}
		}))
		defer server.Close()
		cfg = openai.Config{APIKey: "local-demo", Model: "mock", BaseURL: server.URL + "/v1"}
	}
	c, err := openai.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req := model.Request{Messages: []*msg.Msg{msg.NewText("user", msg.RoleUser, "请用一句话介绍你自己。")}}
	var response *model.Response
	if *stream {
		response, err = c.Stream(ctx, req, func(e msg.StreamEvent) error {
			if e.Type == msg.BlockDelta {
				fmt.Print(e.Delta)
			}
			return nil
		})
		fmt.Println()
	} else {
		response, err = c.Generate(ctx, req)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("完成原因：%s\n完整回复：%s\n", response.FinishReason, response.Message.Text())
}
