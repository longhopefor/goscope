package msg

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestRoundTripAndConversation(t *testing.T) {
	call := New("assistant", RoleAssistant, Text("查天气"), Thinking("选择工具", "sig"),
		Image(Source{Kind: "url", URL: "https://example.com/a.png"}),
		Audio(Source{Kind: "base64", MediaType: "audio/wav", Data: "AAAA"}),
		ToolUse("t1", "weather", json.RawMessage(`{"id":9007199254740993}`)))
	result := New("weather", RoleTool, ToolResult("t1", Text("晴"), Image(Source{Kind: "url", URL: "https://example.com/weather.png"})))
	for _, src := range []*Msg{call, result} {
		raw, err := json.Marshal(src)
		check(t, err)
		var dst Msg
		check(t, json.Unmarshal(raw, &dst))
		if len(dst.Blocks) != len(src.Blocks) {
			t.Fatal("lost blocks")
		}
		again, err := json.Marshal(&dst)
		check(t, err)
		if string(raw) != string(again) {
			t.Fatal("roundtrip changed JSON")
		}
	}
	check(t, ValidateConversation([]*Msg{call, result}, true))
	if !strings.Contains(string(call.Blocks[4].ToolUse.Input), "9007199254740993") {
		t.Fatal("lost integer precision")
	}
}
func TestValidation(t *testing.T) {
	cases := map[string]Block{
		"missing payload":    {Type: BlockText},
		"unknown":            {Type: "table", Text: &TextBlock{}},
		"mismatch":           {Type: BlockImage, Text: &TextBlock{}},
		"multiple":           {Type: BlockText, Text: &TextBlock{}, Image: &ImageBlock{}},
		"invalid source":     Image(Source{Kind: "url", URL: "javascript:alert(1)"}),
		"conflicting source": Image(Source{Kind: "url", URL: "https://example.com", Data: "AAAA"}),
		"invalid base64":     Audio(Source{Kind: "base64", Data: "!", MediaType: "audio/wav"}),
		"partial parameters": ToolUse("a", "t", json.RawMessage(`{"city":`)),
		"null parameters":    ToolUse("a", "t", json.RawMessage(`null`)),
		"array parameters":   ToolUse("a", "t", json.RawMessage(`[]`)),
		"empty name":         ToolUse("a", "", json.RawMessage(`{}`)),
		"recursive output":   ToolResult("a", ToolResult("b", Text("x"))),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if b.Validate() == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	for _, m := range []*Msg{
		New("user", RoleUser, Thinking("x", "")), New("system", RoleSystem, Image(Source{Kind: "url", URL: "https://example.com"})),
		New("tool", RoleTool, Text("x")), New("assistant", RoleAssistant, ToolResult("a", Text("x"))), New("x", "bad", Text("x")), New("x", RoleUser),
	} {
		if m.Validate() == nil {
			t.Fatalf("accepted invalid message: %+v", m)
		}
	}
}
func TestDecodeFailureIsAtomic(t *testing.T) {
	m := NewText("user", RoleUser, "original")
	raw, err := json.Marshal(m)
	check(t, err)
	malformed := strings.Replace(string(raw), `"type":"text"`, `"type":"unknown"`, 1)
	if json.Unmarshal([]byte(malformed), m) == nil {
		t.Fatal("accepted unknown type")
	}
	after, err := json.Marshal(m)
	check(t, err)
	if string(after) != string(raw) {
		t.Fatal("failed decode mutated receiver")
	}
}
func TestCloneDeepIsolation(t *testing.T) {
	src := New("tool", RoleTool, ToolResult("a", Text("original"), Image(Source{Kind: "url", URL: "https://example.com"})))
	src.Metadata = map[string]json.RawMessage{"nested": json.RawMessage(`{"n":9007199254740993,"a":[1]}`)}
	c, err := src.Clone()
	check(t, err)
	c.Blocks[0].ToolResult.Output[0].Text.Text = "changed"
	c.Blocks[0].ToolResult.Output[1].Image.Source.URL = "https://changed.example"
	c.Metadata["nested"][0] = '['
	if src.Blocks[0].ToolResult.Output[0].Text.Text != "original" || src.Blocks[0].ToolResult.Output[1].Image.Source.URL != "https://example.com" || !json.Valid(src.Metadata["nested"]) {
		t.Fatal("clone shares mutable data")
	}
	call := New("a", RoleAssistant, ToolUse("a", "t", json.RawMessage(`{"x":1}`)))
	cc, err := call.Clone()
	check(t, err)
	cc.Blocks[0].ToolUse.Input[0] = '['
	if !json.Valid(call.Blocks[0].ToolUse.Input) {
		t.Fatal("shared tool input")
	}
	src.Metadata["invalid"] = json.RawMessage(`{`)
	if _, err := src.Clone(); err == nil {
		t.Fatal("invalid clone should fail")
	}
}
func TestConversationPairing(t *testing.T) {
	call := New("a", RoleAssistant, ToolUse("a", "t", json.RawMessage(`{}`)), ToolUse("b", "t", json.RawMessage(`{}`)))
	ra := New("t", RoleTool, ToolResult("a", Text("a")))
	rb := New("t", RoleTool, ToolResult("b", Text("b")))
	check(t, ValidateConversation([]*Msg{call, rb, ra}, true))
	check(t, ValidateConversation([]*Msg{call}, false))
	for _, history := range [][]*Msg{{ra}, {call, ra, ra}, {call, ra}, {call, call}} {
		if ValidateConversation(history, true) == nil {
			t.Fatal("accepted inconsistent history")
		}
	}
}
func TestTextEmptyBlocksPreserved(t *testing.T) {
	m := New("u", RoleUser, Text(""), Text("a"), Text(""))
	if m.Text() != "\na\n" {
		t.Fatalf("Text=%q", m.Text())
	}
	if !reflect.DeepEqual(m.BlocksOfType(BlockText), m.Blocks) {
		t.Fatal("filter changed blocks")
	}
}
