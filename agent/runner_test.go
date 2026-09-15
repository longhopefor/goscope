package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/tool"
)

type strategyFunc func(context.Context, ExecutionRequest) (*Result, error)

func (f strategyFunc) Execute(ctx context.Context, req ExecutionRequest) (*Result, error) {
	return f(ctx, req)
}

func TestRunnerOwnsLifecycleAndCopiesInput(t *testing.T) {
	original := input()
	var late func(Event)
	var events []Event
	r, _ := NewRunner(strategyFunc(func(ctx context.Context, req ExecutionRequest) (*Result, error) {
		req.Messages[0].Blocks[0].Text.Text = "changed"
		late = req.Emit
		req.Emit(Event{Type: RunStarted})
		req.Emit(Event{Type: RunFinished})
		req.Emit(Event{Type: ModelStarted, RunID: "forged", Sequence: 900, StopReason: Failed, Step: 1})
		return &Result{Final: msg.NewText("assistant", msg.RoleAssistant, "direct"), RunID: "forged", HookFailures: 900}, nil
	}))
	result, err := r.Run(context.Background(), RunRequest{Messages: original, Hook: func(e Event) error { events = append(events, e); return errors.New("observer") }})
	late(Event{Type: ToolFinished})
	if err != nil || result.StopReason != Completed || result.Final.Text() != "direct" || len(events) != 3 || result.HookFailures != 3 {
		t.Fatalf("%+v %v events=%v", result, err, events)
	}
	if original[0].Text() == "changed" {
		t.Fatal("input was mutated")
	}
	for i, e := range events {
		if e.RunID != result.RunID || e.Sequence != i+1 || e.Time.IsZero() {
			t.Fatal(events)
		}
	}
	if events[1].StopReason != "" || events[2].StopReason != Completed {
		t.Fatal(events)
	}
}
func TestRunnerFailurePaths(t *testing.T) {
	sentinel := errors.New("business")
	for _, kind := range []string{"error", "nil", "panic", "cancel", "timeout", "invalid", "before", "start_hook", "negative", "limit"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			r, _ := NewRunner(strategyFunc(func(ctx context.Context, req ExecutionRequest) (*Result, error) {
				calls++
				switch kind {
				case "nil":
					return nil, nil
				case "panic":
					panic("private panic payload")
				case "cancel":
					cancel()
					return &Result{}, sentinel
				case "timeout":
					<-ctx.Done()
					return &Result{}, ctx.Err()
				case "limit":
					return &Result{}, fmt.Errorf("limit: %w", ErrMaxSteps)
				default:
					return &Result{}, fmt.Errorf("wrapped: %w", sentinel)
				}
			}))
			req := RunRequest{Messages: input()}
			if kind == "invalid" {
				req.Messages = nil
			}
			if kind == "negative" {
				req.Timeouts.Tool = -1
			}
			if kind == "before" {
				cancel()
			}
			if kind == "timeout" {
				req.Timeouts.Run = 10 * time.Millisecond
			}
			var events []Event
			req.Hook = func(e Event) error {
				events = append(events, e)
				if kind == "start_hook" && e.Type == RunStarted {
					cancel()
				}
				return nil
			}
			result, err := r.Run(ctx, req)
			if err == nil || result == nil || len(events) != 2 || events[1].Type != RunFinished || events[1].StopReason != result.StopReason {
				t.Fatalf("%+v %v %v", result, err, events)
			}
			if kind == "error" && !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
			if kind == "cancel" && (!errors.Is(err, sentinel) || !errors.Is(err, context.Canceled)) {
				t.Fatal(err)
			}
			if kind == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if (kind == "invalid" || kind == "before" || kind == "start_hook" || kind == "negative") && calls != 0 {
				t.Fatal("strategy invoked")
			}
		})
	}
	var typedNil strategyFunc
	if _, err := NewRunner(typedNil); err == nil {
		t.Fatal("accepted typed nil")
	}
	var nilRunner *Runner
	if result, err := nilRunner.Run(context.Background(), RunRequest{}); result == nil || err == nil {
		t.Fatal("nil runner")
	}
}
func TestRunnerConcurrentRunsAndContextRelease(t *testing.T) {
	r, _ := NewRunner(strategyFunc(func(ctx context.Context, req ExecutionRequest) (*Result, error) {
		// Joined workers are allowed; event delivery remains serial.
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); req.Emit(Event{Type: ToolStarted}) }()
		}
		wg.Wait()
		return &Result{}, nil
	}))
	var wg sync.WaitGroup
	var ids sync.Map
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var events []Event
			result, err := r.Run(context.Background(), RunRequest{Messages: input(), Hook: func(e Event) error { events = append(events, e); return nil }})
			if err != nil || len(events) != 5 {
				t.Errorf("%v %v", events, err)
				return
			}
			if _, exists := ids.LoadOrStore(result.RunID, true); exists {
				t.Error("duplicate run ID")
			}
			for j, e := range events {
				if e.Sequence != j+1 || e.RunID != result.RunID {
					t.Error("mixed events")
				}
			}
		}()
	}
	wg.Wait()
	var child context.Context
	r, _ = NewRunner(strategyFunc(func(ctx context.Context, _ ExecutionRequest) (*Result, error) { child = ctx; return &Result{}, nil }))
	_, err := r.Run(context.Background(), RunRequest{Messages: input()})
	if err != nil || child.Err() == nil {
		t.Fatal("run context not released")
	}
}
func TestRunnerPreservesPartialBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	partial := &tool.BatchResult{Calls: []tool.CallResult{{CallID: "A", Outcome: tool.Succeeded}, {CallID: "B", Outcome: tool.Unknown}, {CallID: "C", Outcome: tool.NotStarted}}, Message: msg.New("tools", msg.RoleTool, msg.ToolResult("A", msg.Text("42")))}
	r, _ := NewRunner(strategyFunc(func(context.Context, ExecutionRequest) (*Result, error) {
		cancel()
		return &Result{ToolBatches: []*tool.BatchResult{partial}}, context.Canceled
	}))
	result, err := r.Run(ctx, RunRequest{Messages: input()})
	if !errors.Is(err, context.Canceled) || result.StopReason != Canceled || result.ToolBatches[0] != partial {
		t.Fatalf("%+v %v", result, err)
	}
}
