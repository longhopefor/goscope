package msg

import (
	"encoding/json"
	"testing"
	"time"
)

// 只比较当前单文本 JSON 往返，不代表整个 Agent 或其他框架性能。
type FlatMsg struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

func BenchmarkJSONRoundTrip_Flat(b *testing.B) {
	src := FlatMsg{ID: "fixed", Name: "user", Role: "user", Text: "北京天气", Timestamp: time.Now()}
	for i := 0; i < b.N; i++ {
		raw, err := json.Marshal(src)
		if err != nil {
			b.Fatal(err)
		}
		var dst FlatMsg
		if err := json.Unmarshal(raw, &dst); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkJSONRoundTrip_Blocks(b *testing.B) {
	src := NewText("user", RoleUser, "北京天气")
	for i := 0; i < b.N; i++ {
		raw, err := json.Marshal(src)
		if err != nil {
			b.Fatal(err)
		}
		var dst Msg
		if err := json.Unmarshal(raw, &dst); err != nil {
			b.Fatal(err)
		}
	}
}
