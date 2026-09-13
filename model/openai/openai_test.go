package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func request() model.Request {
	return model.Request{Messages: []*msg.Msg{msg.NewText("user", msg.RoleUser, "你好")}}
}
func client(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, err := New(Config{APIKey: "test-secret", Model: "test-model", BaseURL: s.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestFormatterMapping(t *testing.T) {
	q := msg.New("u", msg.RoleUser, msg.Text("看图"), msg.Image(msg.Source{Kind: "base64", Data: "AAAA", MediaType: "image/png"}))
	a := msg.New("a", msg.RoleAssistant, msg.ToolUse("a", "weather", json.RawMessage(`{"id":9007199254740993}`)), msg.ToolUse("b", "weather", json.RawMessage(`{}`)))
	r := model.Request{Messages: []*msg.Msg{q, a, msg.New("t", msg.RoleTool, msg.ToolResult("b", msg.Text("B")), msg.ToolResult("a", msg.Text("A")))}, Tools: []model.Tool{{Name: "weather", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}}
	raw, err := (Formatter{}).Encode(r, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Messages []struct {
			Role       string
			Content    json.RawMessage
			ToolCallID string     `json:"tool_call_id"`
			ToolCalls  []toolCall `json:"tool_calls"`
		}
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 4 || got.Messages[2].ToolCallID != "b" || got.Messages[3].ToolCallID != "a" {
		t.Fatal(string(raw))
	}
	if got.Messages[1].ToolCalls[0].Function.Arguments != `{"id":9007199254740993}` {
		t.Fatal("arguments changed")
	}
	if !strings.Contains(string(got.Messages[0].Content), "data:image/png;base64,AAAA") {
		t.Fatal("image mapping")
	}
	// 不修改调用方历史或参数。
	if len(r.Messages) != 3 || string(a.Blocks[0].ToolUse.Input) != `{"id":9007199254740993}` {
		t.Fatal("input mutated")
	}
}
func TestFormatterRejectsLossyInput(t *testing.T) {
	for _, m := range []*msg.Msg{
		msg.New("a", msg.RoleAssistant, msg.Thinking("reason", "sig")),
		msg.New("u", msg.RoleUser, msg.Audio(msg.Source{Kind: "base64", Data: "AAAA", MediaType: "audio/wav"})),
	} {
		if _, err := (Formatter{}).Encode(model.Request{Messages: []*msg.Msg{m}}, "m", false); err == nil {
			t.Fatal("unsupported input accepted")
		}
	}
	a := msg.New("a", msg.RoleAssistant, msg.ToolUse("c", "t", json.RawMessage(`{}`)))
	r := msg.New("t", msg.RoleTool, msg.ToolResult("c", msg.Text("ok")))
	if _, err := (Formatter{}).Encode(model.Request{Messages: []*msg.Msg{a, msg.NewText("u", msg.RoleUser, "interrupt"), r}}, "m", false); err == nil {
		t.Fatal("interrupted tools accepted")
	}
	r.Blocks[0].ToolResult.Output = []msg.Block{msg.Image(msg.Source{Kind: "url", URL: "https://example.com/a"})}
	if _, err := (Formatter{}).Encode(model.Request{Messages: []*msg.Msg{a, r}}, "m", false); err == nil {
		t.Fatal("multimodal tool result dropped")
	}
}

const answer = `{"id":"r1","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`

func TestGenerateHTTP(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("bad request")
		}
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"stream":false`) {
			t.Error("stream flag")
		}
		fmt.Fprint(w, answer)
	})
	out, err := c.Generate(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if out.Message.Text() != "你好" || out.Usage.TotalTokens != 3 || out.ID != "r1" {
		t.Fatal(out)
	}
}
func TestHTTPErrorAndCancellation(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "req1")
		w.WriteHeader(429)
		fmt.Fprint(w, "test-secret")
	})
	_, err := c.Generate(context.Background(), request())
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != 429 || strings.Contains(err.Error(), "test-secret") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Generate(ctx, request()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func chunk(delta string, finish string) string {
	return fmt.Sprintf("data: {\"id\":\"r\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":%s}]}\r\n\r\n", delta, finish)
}
func toolDelta(index int, id, name, args string) string {
	b, _ := json.Marshal(map[string]any{"tool_calls": []any{map[string]any{"index": index, "id": id, "type": "function", "function": map[string]string{"name": name, "arguments": args}}}})
	return string(b)
}
func TestStreamToolsAndUsage(t *testing.T) {
	body := ": comment\r\n\r\n" + chunk(`{"role":"assistant","content":"查询"}`, "null") +
		chunk(toolDelta(1, "b", "weather", `{"city":`), "null") + chunk(toolDelta(0, "a", "weather", `{}`), "null") +
		chunk(toolDelta(1, "", "", `"北京"}`), "null") + chunk(`{}`, `"tool_calls"`) +
		"data: {\"id\":\"r\",\"choices\":[],\"usage\":{\"total_tokens\":10}}\r\n\r\ndata: [DONE]\r\n\r\n"
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	})
	mirror := msg.NewAggregator("assistant")
	out, err := c.Stream(context.Background(), request(), mirror.Apply)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := mirror.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(copied.Blocks, out.Message.Blocks) {
		t.Fatal("events disagree with response")
	}
	if len(out.Message.Blocks) != 3 || out.Message.Blocks[1].ToolUse.ID != "b" || string(out.Message.Blocks[1].ToolUse.Input) != `{"city":"北京"}` || out.Usage.TotalTokens != 10 {
		t.Fatal(out)
	}
}
func TestStreamFailures(t *testing.T) {
	tests := map[string]string{
		"truncated":          chunk(`{"content":"partial"}`, "null"),
		"early done":         "data: [DONE]\n\n",
		"length":             chunk(`{"content":"partial"}`, `"length"`) + "data: [DONE]\n\n",
		"invalid JSON":       "data: {bad}\n\n",
		"invalid args":       chunk(toolDelta(0, "a", "t", `{"x":`), "null") + chunk(`{}`, `"tool_calls"`) + "data: [DONE]\n\n",
		"tool without index": chunk(`{"tool_calls":[{"id":"a","type":"function","function":{"name":"t","arguments":"{}"}}]}`, "null"),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			c := client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, body)
			})
			out, err := c.Stream(context.Background(), request(), nil)
			if err == nil || out != nil {
				t.Fatal("partial response accepted")
			}
		})
	}
}
func TestCallbackFailureAndDeadline(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, chunk(`{"content":"x"}`, "null"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	sentinel := errors.New("stop callback")
	if _, err := c.Stream(context.Background(), request(), func(msg.StreamEvent) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Stream(ctx, request(), nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestDecodeRefusalAndTruncation(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(answer, `"stop"`, `"length"`, 1),
		strings.Replace(answer, `"content":"你好"`, `"content":null,"refusal":"no"`, 1),
		`{"choices":[]}`, `{"error":{"message":"failure"}}`,
	} {
		if _, err := (Formatter{}).Decode([]byte(raw)); err == nil {
			t.Fatal("bad response accepted")
		}
	}
}
func TestSSEMultiline(t *testing.T) {
	var got string
	err := readSSE(strings.NewReader("data: hello\ndata: world\n\ndata: [DONE]\n\n"), func(s string) (bool, error) {
		if s == "[DONE]" {
			return true, nil
		}
		got = s
		return false, nil
	})
	if err != nil || got != "hello\nworld" {
		t.Fatal(got, err)
	}
}
