package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/longhopefor/goscope/agent"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/tool"
)

func input() model.Request {
	return model.Request{Messages: []*msg.Msg{msg.NewText("user", msg.RoleUser, "hello")}}
}
func TestFormatterToolRound(t *testing.T) {
	r := model.Request{Messages: []*msg.Msg{msg.NewText("sys", msg.RoleSystem, "be concise"), msg.NewText("user", msg.RoleUser, "add"), msg.New("assistant", msg.RoleAssistant, msg.Text("calculate"), msg.ToolUse("A", "add", []byte(`{"n":9007199254740993}`))), msg.New("tools", msg.RoleTool, msg.ToolResult("A", msg.Text("42")))}, Tools: []model.Tool{{Name: "add", Parameters: []byte(`{"type":"object"}`)}}}
	r.Messages[3].Blocks[0].ToolResult.IsError = true
	raw, err := (Formatter{MaxTokens: 123}).Encode(r, "test", true)
	if err != nil {
		t.Fatal(err)
	}
	var w request
	if err = json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	if w.MaxTokens != 123 || len(w.System) != 1 || w.Messages[2].Role != "user" || !w.Messages[2].Content[0].IsError || string(w.Messages[1].Content[1].Input) != `{"n":9007199254740993}` {
		t.Fatal(string(raw))
	}
	r.Messages = append(r.Messages, msg.NewText("sys", msg.RoleSystem, "late"))
	if _, err = (Formatter{}).Encode(r, "test", false); err == nil {
		t.Fatal("late system accepted")
	}
}
func TestDecodeAndUnsupported(t *testing.T) {
	good := `{"id":"m","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3,"cache_read_input_tokens":4}}`
	r, err := (Formatter{}).Decode([]byte(good))
	if err != nil || r.Message.Text() != "ok" || r.Usage.TotalTokens != 9 {
		t.Fatalf("%+v %v", r, err)
	}
	for _, raw := range []string{strings.Replace(good, "end_turn", "max_tokens", 1), strings.Replace(good, "end_turn", "tool_use", 1), strings.Replace(good, `"type":"text"`, `"type":"thinking"`, 1), `{"type":"error","error":{"message":"private"}}`} {
		if _, err := (Formatter{}).Decode([]byte(raw)); err == nil {
			t.Fatal(raw)
		}
	}
}
func frame(v any) string { raw, _ := json.Marshal(v); return "data: " + string(raw) + "\n\n" }
func streamFrames(toolCall bool) string {
	s := frame(map[string]any{"type": "message_start", "message": map[string]any{"id": "m", "type": "message", "role": "assistant", "model": "test", "content": []any{}, "usage": map[string]int{"input_tokens": 5, "output_tokens": 1}}})
	b := map[string]any{"type": "text", "text": ""}
	delta := map[string]any{"type": "text_delta", "text": "42"}
	reason := "end_turn"
	if toolCall {
		b = map[string]any{"type": "tool_use", "id": "A", "name": "add", "input": map[string]any{}}
		delta = map[string]any{"type": "input_json_delta", "partial_json": `{"a":20,`}
		reason = "tool_use"
	}
	s += frame(map[string]any{"type": "content_block_start", "index": 0, "content_block": b})
	s += frame(map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
	if toolCall {
		s += frame(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": `"b":22}`}})
	}
	s += frame(map[string]any{"type": "content_block_stop", "index": 0})
	s += frame(map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": reason}, "usage": map[string]int{"output_tokens": 3}})
	s += frame(map[string]any{"type": "message_delta", "delta": map[string]any{}, "usage": map[string]any{"output_tokens": 4, "input_tokens": nil, "cache_creation": map[string]int{"ephemeral_5m_input_tokens": 0}}})
	return s + frame(map[string]string{"type": "message_stop"})
}
func clientFor(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	c, err := New(Config{APIKey: "test-secret", Model: "test", BaseURL: server.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestRunnerIntegration(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			requests := 0
			c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "test-secret" || r.Header.Get("anthropic-version") != "2023-06-01" {
					t.Error("request headers/path")
				}
				var req request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.Stream != stream || req.MaxTokens != 4096 {
					t.Error("request options")
				}
				if requests == 2 {
					last := req.Messages[len(req.Messages)-1]
					if last.Role != "user" || last.Content[0].Type != "tool_result" || last.Content[0].Content[0].Text != "42" {
						t.Errorf("bad tool result %+v", last)
					}
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, streamFrames(requests == 1))
					return
				}
				content := []block{{Type: "text", Text: "42"}}
				reason := "end_turn"
				if requests == 1 {
					content = []block{{Type: "tool_use", ID: "A", Name: "add", Input: []byte(`{"a":20,"b":22}`)}}
					reason = "tool_use"
				}
				json.NewEncoder(w).Encode(envelope{Type: "message", ID: "m", Role: "assistant", Model: "test", Content: content, StopReason: reason})
			})
			type args struct {
				A int `json:"a"`
				B int `json:"b"`
			}
			calls := 0
			add, _ := tool.New("add", "add", func(_ context.Context, a args) ([]msg.Block, error) {
				calls++
				return []msg.Block{msg.Text(fmt.Sprint(a.A + a.B))}, nil
			})
			reg, _ := tool.NewRegistry(add)
			react, _ := agent.New(c, reg, 3)
			runner, _ := agent.NewRunner(react)
			var result *agent.Result
			var err error
			if stream {
				s, startErr := runner.Stream(context.Background(), agent.RunRequest{Messages: input().Messages})
				if startErr != nil {
					t.Fatal(startErr)
				}
				defer s.Close()
				for {
					_, readErr := s.Recv()
					if readErr != nil {
						break
					}
				}
				result, err = s.Wait()
			} else {
				result, err = runner.Run(context.Background(), agent.RunRequest{Messages: input().Messages})
			}
			if err != nil || result.Final == nil || result.Final.Text() != "42" || calls != 1 || requests != 2 {
				t.Fatalf("%+v %v calls=%d requests=%d", result, err, calls, requests)
			}
		})
	}
}
func TestStreamValidation(t *testing.T) {
	good := streamFrames(false)
	for _, tc := range []struct {
		name, body string
		bad        bool
	}{
		{"valid", good, false},
		{"eof", strings.TrimSuffix(good, frame(map[string]string{"type": "message_stop"})), true},
		{"early", frame(map[string]string{"type": "message_stop"}), true},
		{"index", strings.Replace(good, `"index":0`, `"index":4`, 1), true},
		{"unsupported", strings.Replace(good, `"type":"text_delta"`, `"type":"thinking_delta"`, 1), true},
		{"truncated", strings.Replace(good, "end_turn", "max_tokens", 1), true},
		{"error", frame(map[string]string{"type": "error", "message": "private"}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.body)
			})
			result, err := c.Stream(context.Background(), input(), nil)
			if (err != nil) != tc.bad {
				t.Fatalf("%+v %v", result, err)
			}
			if !tc.bad && (result.Usage.PromptTokens != 5 || result.Usage.CompletionTokens != 4) {
				t.Fatal(result.Usage)
			}
		})
	}
}
func TestHTTPErrorRedirectAndCallback(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("request-id", "req-test")
		w.WriteHeader(429)
		fmt.Fprint(w, "private")
	})
	_, err := c.Generate(context.Background(), input())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 429 || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer destination.Close()
	c = clientFor(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) })
	if _, err = c.Generate(context.Background(), input()); err == nil || redirected {
		t.Fatal("redirect followed")
	}
	sentinel := errors.New("consumer closed")
	c = clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, streamFrames(false))
	})
	if _, err = c.Stream(context.Background(), input(), func(msg.StreamEvent) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}
func TestCloseCancelsHTTP(t *testing.T) {
	exited := make(chan struct{})
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		body := streamFrames(false)
		parts := strings.Split(body, "\n\n")
		fmt.Fprint(w, strings.Join(parts[:2], "\n\n")+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(exited)
	})
	reg, _ := tool.NewRegistry()
	react, _ := agent.New(c, reg, 2)
	runner, _ := agent.NewRunner(react)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := runner.Stream(ctx, agent.RunRequest{Messages: input().Messages})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Recv(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("HTTP body not released")
	}
	_, err = s.Wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestSSEFraming(t *testing.T) {
	body := "event: ping\r\ndata: {\"type\":\r\ndata: \"ping\"}\r\n\r\n"
	if err := readSSE(strings.NewReader(body), func(name, data string) (bool, error) {
		if name != "ping" || !json.Valid([]byte(data)) {
			t.Fatal(name, data)
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := readSSE(strings.NewReader(""), func(string, string) (bool, error) { return false, nil }); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
}

func TestImagesEmptyTextAndInvalidInputs(t *testing.T) {
	image := msg.Image(msg.ImageSource{Kind: "base64", MediaType: "image/png", Data: "aGk="})
	req := model.Request{Messages: []*msg.Msg{msg.New("user", msg.RoleUser, msg.Text(""), image)}}
	raw, err := (Formatter{}).Encode(req, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"text":""`) || !strings.Contains(string(raw), `"media_type":"image/png"`) {
		t.Fatal(string(raw))
	}
	req.Messages[0].Blocks[1].Image.Source.MediaType = "image/bmp"
	if _, err = (Formatter{}).Encode(req, "test", false); err == nil {
		t.Fatal("unsupported media accepted")
	}
	for _, config := range []Config{{Model: "test"}, {APIKey: "k", Model: "m", MaxTokens: -1}, {APIKey: "k", Model: "m", BaseURL: "http://example.com/v1"}, {APIKey: "k", Model: "m", BaseURL: "https://user:pass@example.com/v1"}} {
		if _, err := New(config); err == nil {
			t.Fatal(config)
		}
	}
}

func TestMalformedToolStreamNeverExecutes(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, strings.Replace(streamFrames(true), "22}", "22", 1))
	})
	called := false
	add, _ := tool.New("add", "add", func(context.Context, struct{}) ([]msg.Block, error) { called = true; return nil, nil })
	registry, _ := tool.NewRegistry(add)
	react, _ := agent.New(c, registry, 2)
	runner, _ := agent.NewRunner(react)
	s, err := runner.Stream(context.Background(), agent.RunRequest{Messages: input().Messages})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for {
		_, err = s.Recv()
		if err != nil {
			break
		}
	}
	result, err := s.Wait()
	if err == nil || called || result.Final != nil {
		t.Fatalf("%+v %v called=%v", result, err, called)
	}
}
