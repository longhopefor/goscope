package tool

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/model/openai"
	"github.com/longhopefor/goscope/msg"
	"strings"
	"testing"
)

type args struct {
	Count   int64  `json:"count" tool:"required,min=0"`
	Enabled bool   `json:"enabled" tool:"required"`
	Mode    string `json:"mode" tool:"enum=fast|slow"`
	Items   []item `json:"items"`
}
type item struct {
	Value int `json:"value" tool:"required,min=1,max=5"`
}

func newTest(t *testing.T, fn func(context.Context, args) ([]msg.Block, error)) Tool {
	t.Helper()
	x, err := New("test", "test", fn)
	if err != nil {
		t.Fatal(err)
	}
	return x
}
func TestValidationAndPrecision(t *testing.T) {
	var got args
	calls := 0
	x := newTest(t, func(_ context.Context, a args) ([]msg.Block, error) {
		calls++
		got = a
		return []msg.Block{msg.Text("ok")}, nil
	})
	for _, raw := range []string{`{"count":0,"enabled":false}`, `{"count":9007199254740993,"enabled":true,"mode":"fast","items":[{"value":3}]}`} {
		if _, err := x.Call(context.Background(), json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if got.Count != 9007199254740993 || calls != 2 {
		t.Fatal("precision or zero values")
	}
	for _, raw := range []string{
		`{}`, `{"count":0}`, `{"count":null,"enabled":true}`, `{"Count":0,"enabled":false}`, `{"count":0,"enabled":false,"extra":1}`,
		`{"count":1,"count":2,"enabled":true}`, `{"count":-1,"enabled":true}`, `{"count":1.5,"enabled":true}`, `{"count":9223372036854775808,"enabled":true}`,
		`{"count":0,"enabled":false,"mode":"bad"}`, `{"count":0,"enabled":false,"items":[{}]}`, `{"count":0,"enabled":false,"items":[{"value":9}]}`,
		`{"count":0,"enabled":false} {}`, `null`, `[]`, `{"count":0,"enabled":false,"items":null}`,
	} {
		if _, err := x.Call(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if calls != 2 {
		t.Fatal("invalid parameters reached function")
	}
	var schema map[string]any
	if err := json.Unmarshal(x.Definition().Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false || len(schema["required"].([]any)) != 2 {
		t.Fatal(schema)
	}
	d := x.Definition()
	d.Parameters[0] = '['
	if !json.Valid(x.Definition().Parameters) {
		t.Fatal("definition alias")
	}
}
func TestRegistrationRejectsUnsupported(t *testing.T) {
	_, err := New("bad", "", func(context.Context, map[string]string) ([]msg.Block, error) { return nil, nil })
	if err == nil {
		t.Fatal("map accepted")
	}
	type Bad struct {
		X *int `json:"x"`
	}
	_, err = New("bad", "", func(context.Context, Bad) ([]msg.Block, error) { return nil, nil })
	if err == nil {
		t.Fatal("pointer accepted")
	}
	type BadTag struct {
		X int `json:"x" tool:"min=2,max=1"`
	}
	_, err = New("bad", "", func(context.Context, BadTag) ([]msg.Block, error) { return nil, nil })
	if err == nil {
		t.Fatal("bad tag accepted")
	}
	type Typo struct {
		X string `json:"x" tool:"requried"`
	}
	_, err = New("bad", "", func(context.Context, Typo) ([]msg.Block, error) { return nil, nil })
	if err == nil {
		t.Fatal("typo accepted")
	}
	x := newTest(t, func(context.Context, args) ([]msg.Block, error) { return nil, nil })
	if _, err := NewRegistry(x, x); err == nil {
		t.Fatal("duplicate tool accepted")
	}
}
func TestExecuteFailuresAndIntegration(t *testing.T) {
	x := newTest(t, func(_ context.Context, a args) ([]msg.Block, error) {
		if a.Count == 1 {
			return nil, errors.New("secret backend detail")
		}
		if a.Count == 2 {
			panic("secret panic")
		}
		return []msg.Block{msg.Text("ok")}, nil
	})
	r, _ := NewRegistry(x)
	q := msg.New("a", msg.RoleAssistant,
		msg.ToolUse("1", "test", json.RawMessage(`{"count":1,"enabled":true}`)),
		msg.ToolUse("2", "test", json.RawMessage(`{"count":2,"enabled":true}`)),
		msg.ToolUse("3", "test", json.RawMessage(`{}`)),
		msg.ToolUse("4", "missing", json.RawMessage(`{}`)),
		msg.ToolUse("5", "test", json.RawMessage(`{"count":0,"enabled":false}`)),
	)
	out, err := r.Execute(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range out.Blocks {
		if b.ToolResult.ToolUseID != q.Blocks[i].ToolUse.ID || b.ToolResult.IsError != (i < 4) {
			t.Fatal("result pairing")
		}
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("leaked error")
	}
	if err := msg.ValidateConversation([]*msg.Msg{q, out}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := (openai.Formatter{}).Encode(model.Request{Messages: []*msg.Msg{q, out}, Tools: r.Definitions()}, "test", false); err != nil {
		t.Fatal(err)
	}
	defs := r.Definitions()
	defs[0].Parameters[0] = '['
	if !json.Valid(r.Definitions()[0].Parameters) {
		t.Fatal("registry alias")
	}
}
func TestCancellationStopsBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	count := 0
	x := newTest(t, func(ctx context.Context, _ args) ([]msg.Block, error) { count++; cancel(); return nil, ctx.Err() })
	r, _ := NewRegistry(x)
	q := msg.New("a", msg.RoleAssistant, msg.ToolUse("1", "test", json.RawMessage(`{"count":0,"enabled":false}`)), msg.ToolUse("2", "test", json.RawMessage(`{"count":0,"enabled":false}`)))
	out, err := r.Execute(ctx, q)
	if !errors.Is(err, context.Canceled) || out != nil || count != 1 {
		t.Fatal(count, err)
	}
}
func TestOutputIsolationAndValidation(t *testing.T) {
	shared := []msg.Block{msg.Text("original")}
	x := newTest(t, func(context.Context, args) ([]msg.Block, error) { return shared, nil })
	out, err := x.Call(context.Background(), json.RawMessage(`{"count":0,"enabled":false}`))
	if err != nil {
		t.Fatal(err)
	}
	out[0].Text.Text = "changed"
	if shared[0].Text.Text != "original" {
		t.Fatal("shared output")
	}
	bad := newTest(t, func(context.Context, args) ([]msg.Block, error) {
		return []msg.Block{msg.ToolResult("x", msg.Text("nested"))}, nil
	})
	if _, err := bad.Call(context.Background(), json.RawMessage(`{"count":0,"enabled":false}`)); err == nil {
		t.Fatal("nested result accepted")
	}
}
