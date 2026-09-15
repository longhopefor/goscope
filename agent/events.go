package agent

import (
	"fmt"
	"time"

	"github.com/longhopefor/goscope/msg"
)

type EventType string

const (
	RunStarted    EventType = "run_started"
	ModelStarted  EventType = "model_started"
	ModelFinished EventType = "model_finished"
	ToolStarted   EventType = "tool_started"
	ToolFinished  EventType = "tool_finished"
	RunFinished   EventType = "run_finished"
)

// Event 只有值字段，不暴露历史、参数或结果的可变引用。
// Status 为 started/succeeded/failed/canceled；RunFinished 使用 StopReason。
type Event struct {
	RunID      string
	Sequence   int
	Time       time.Time
	Type       EventType
	Step       int
	ToolCallID string
	ToolName   string
	Status     string
	StopReason StopReason
}

// Hook 同步调用，不应阻塞。返回错误或 panic 计入 HookFailures，不改变执行结果。
// 需要中止运行时，调用方应使用 context；Hook 不是控制接口。
type Hook func(Event) error

// Timeouts are cooperative. Zero inherits the parent deadline.
type Timeouts struct{ Run, Model, Tool time.Duration }

type RunRequest struct {
	Timeouts Timeouts
	Messages []*msg.Msg
	Hook     Hook
}

func notify(h Hook, e Event) (err error) {
	if h == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("progress hook panicked")
		}
	}()
	return h(e)
}
