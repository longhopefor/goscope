package msg

import (
	"encoding/json"
	"testing"
)

func TestStreamInterleaving(t *testing.T) {
	a := NewAggregator("a")
	for _, e := range []StreamEvent{
		{Type: BlockStart, BlockID: "text", Block: Text("")},
		{Type: BlockStart, BlockID: "call", Block: ToolUse("call_1", "weather", nil)},
		{Type: BlockDelta, BlockID: "call", Delta: `{"city":`},
		{Type: BlockDelta, BlockID: "text", Delta: "查询"},
		{Type: BlockDelta, BlockID: "call", Delta: `"北京"}`},
		{Type: BlockEnd, BlockID: "call"},
		{Type: BlockDelta, BlockID: "text", Delta: "天气"},
		{Type: BlockEnd, BlockID: "text"},
	} {
		check(t, a.Apply(e))
	}
	check(t, a.AddBlock("image", Image(Source{Kind: "url", URL: "https://example.com/a.png"})))
	m, err := a.Finish()
	check(t, err)
	if m.Text() != "查询天气" || m.Blocks[1].ToolUse.ID != "call_1" || string(m.Blocks[1].ToolUse.Input) != `{"city":"北京"}` || m.Blocks[2].Type != BlockImage {
		t.Fatal("wrong aggregation")
	}
	if _, err := a.Finish(); err == nil {
		t.Fatal("finished twice")
	}
	if a.Apply(StreamEvent{Type: BlockDelta, BlockID: "text", Delta: "late"}) == nil {
		t.Fatal("accepted late delta")
	}
}
func TestStreamIncompleteRecovery(t *testing.T) {
	a := NewAggregator("a")
	check(t, a.Apply(StreamEvent{Type: BlockStart, BlockID: "c", Block: ToolUse("c", "t", json.RawMessage(`{"x":`))}))
	if _, err := a.Finish(); err == nil {
		t.Fatal("accepted unfinished block")
	}
	if a.Apply(StreamEvent{Type: BlockEnd, BlockID: "c"}) == nil {
		t.Fatal("accepted invalid JSON")
	}
	check(t, a.Apply(StreamEvent{Type: BlockDelta, BlockID: "c", Delta: `1}`}))
	check(t, a.Apply(StreamEvent{Type: BlockEnd, BlockID: "c"}))
	m, err := a.Finish()
	check(t, err)
	if string(m.Blocks[0].ToolUse.Input) != `{"x":1}` {
		t.Fatal("failed end mutated partial")
	}
}
func TestStreamRejectsInvalidTransitions(t *testing.T) {
	a := NewAggregator("a")
	if a.Apply(StreamEvent{Type: BlockDelta, BlockID: "missing"}) == nil {
		t.Fatal("unknown delta accepted")
	}
	if a.Apply(StreamEvent{Type: BlockStart, BlockID: "bad", Block: Block{Type: BlockText}}) == nil {
		t.Fatal("invalid start accepted")
	}
	// 失败不能占用 ID。
	check(t, a.Apply(StreamEvent{Type: BlockStart, BlockID: "bad", Block: Text("x")}))
	if a.Apply(StreamEvent{Type: BlockStart, BlockID: "bad", Block: Text("x")}) == nil {
		t.Fatal("duplicate start accepted")
	}
	check(t, a.Apply(StreamEvent{Type: BlockEnd, BlockID: "bad"}))
	if a.Apply(StreamEvent{Type: BlockDelta, BlockID: "bad", Delta: "x"}) == nil {
		t.Fatal("delta after end accepted")
	}
	if a.Apply(StreamEvent{Type: BlockEnd, BlockID: "bad"}) == nil {
		t.Fatal("duplicate end accepted")
	}
}
func TestStreamOwnsInput(t *testing.T) {
	a := NewAggregator("a")
	b := Thinking("initial", "sig")
	check(t, a.Apply(StreamEvent{Type: BlockStart, BlockID: "t", Block: b}))
	b.Thinking.Signature = "changed"
	check(t, a.Apply(StreamEvent{Type: BlockEnd, BlockID: "t"}))
	m, err := a.Finish()
	check(t, err)
	if m.Blocks[0].Thinking.Signature != "sig" {
		t.Fatal("shared start payload")
	}
}
