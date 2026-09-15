// anthropic-demo defaults to an in-memory HTTP fixture. -real opts into API use.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/longhopefor/goscope/agent"
	"github.com/longhopefor/goscope/model/anthropic"
	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/tool"
)

type fixture struct{}

func (fixture) RoundTrip(r *http.Request) (*http.Response, error) {
	defer r.Body.Close()
	var request struct {
		Stream   bool              `json:"stream"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return nil, err
	}
	reason := "end_turn"
	content := map[string]any{"type": "text", "text": "20 + 22 = 42"}
	if len(request.Messages) == 1 {
		reason = "tool_use"
		content = map[string]any{"type": "tool_use", "id": "A", "name": "add", "input": map[string]int{"a": 20, "b": 22}}
	}
	message := map[string]any{"id": "fixture-message", "type": "message", "role": "assistant", "model": "fixture", "content": []any{content}, "stop_reason": reason, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}}
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	raw, _ := json.Marshal(message)
	body := string(raw)
	if request.Stream {
		header.Set("Content-Type", "text/event-stream")
		var out strings.Builder
		emit := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(&out, "data: %s\n\n", b) }
		message["content"] = []any{}
		message["stop_reason"] = nil
		emit(map[string]any{"type": "message_start", "message": message})
		delta := map[string]any{"type": "text_delta", "text": content["text"]}
		content["text"] = ""
		if reason == "tool_use" {
			delete(content, "text")
			content["input"] = map[string]any{}
			delta = map[string]any{"type": "input_json_delta", "partial_json": `{"a":20,"b":22}`}
		}
		emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": content})
		emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
		emit(map[string]any{"type": "content_block_stop", "index": 0})
		emit(map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": reason}, "usage": map[string]int{"output_tokens": 5}})
		emit(map[string]string{"type": "message_stop"})
		body = out.String()
	}
	return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

type args struct {
	A int `json:"a"`
	B int `json:"b"`
}

func run() error {
	real := flag.Bool("real", false, "call the real Anthropic API (requires ANTHROPIC_API_KEY and ANTHROPIC_MODEL)")
	streaming := flag.Bool("stream", false, "stream model output")
	flag.Parse()
	config := anthropic.Config{APIKey: "fixture-key", Model: "fixture", HTTPClient: &http.Client{Transport: fixture{}}}
	if *real {
		config = anthropic.Config{APIKey: os.Getenv("ANTHROPIC_API_KEY"), Model: os.Getenv("ANTHROPIC_MODEL")}
	}
	client, err := anthropic.New(config)
	if err != nil {
		return err
	}
	add, err := tool.New("add", "Add two integers", func(_ context.Context, a args) ([]msg.Block, error) {
		return []msg.Block{msg.Text(fmt.Sprint(a.A + a.B))}, nil
	})
	if err != nil {
		return err
	}
	registry, err := tool.NewRegistry(add)
	if err != nil {
		return err
	}
	react, err := agent.New(client, registry, 3)
	if err != nil {
		return err
	}
	runner, err := agent.NewRunner(react)
	if err != nil {
		return err
	}
	request := agent.RunRequest{Messages: []*msg.Msg{msg.NewText("user", msg.RoleUser, "Use add to calculate 20 + 22.")}, Timeouts: agent.Timeouts{Run: time.Minute, Model: 30 * time.Second}}
	var result *agent.Result
	if *streaming {
		stream, startErr := runner.Stream(context.Background(), request)
		if startErr != nil {
			return startErr
		}
		defer stream.Close()
		for {
			event, readErr := stream.Recv()
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				break
			}
			if event.Event.Type == msg.BlockDelta {
				fmt.Printf("step=%d delta=%q\n", event.Step, event.Event.Delta)
			}
		}
		result, err = stream.Wait()
	} else {
		result, err = runner.Run(context.Background(), request)
	}
	if err != nil {
		return err
	}
	fmt.Printf("stop=%s steps=%d answer=%q\n", result.StopReason, result.Steps, result.Final.Text())
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
