package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
)

type streamModel func(context.Context, model.Request, func(msg.StreamEvent) error) (*model.Response, error)

func (f streamModel) Generate(context.Context, model.Request) (*model.Response, error) {
	panic("Generate must not be called")
}
func (f streamModel) Stream(c context.Context, r model.Request, e func(msg.StreamEvent) error) (*model.Response, error) {
	return f(c, r, e)
}
func streamReply(emit func(msg.StreamEvent) error, events ...msg.StreamEvent) (*model.Response, error) {
	a := msg.NewAggregator("assistant")
	for _, e := range events {
		if err := a.Apply(e); err != nil {
			return nil, err
		}
		if err := emit(e); err != nil {
			return nil, err
		}
	}
	m, err := a.Finish()
	return &model.Response{Message: m}, err
}
func textEvents(s string) []msg.StreamEvent {
	return []msg.StreamEvent{{Type: msg.BlockStart, BlockID: "t", Block: msg.Text("")}, {Type: msg.BlockDelta, BlockID: "t", Delta: s}, {Type: msg.BlockEnd, BlockID: "t"}}
}
func collect(s *Stream) ([]ContentEvent, *Result, error) {
	var events []ContentEvent
	for {
		e, err := s.Recv()
		if err != nil {
			break
		}
		events = append(events, e)
	}
	r, err := s.Wait()
	return events, r, err
}

func TestStreamToolRoundAndTerminal(t *testing.T) {
	calls, round := 0, 0
	reg := registry(t, func(context.Context) ([]msg.Block, error) { calls++; return []msg.Block{msg.Text("42")}, nil })
	m := streamModel(func(ctx context.Context, r model.Request, emit func(msg.StreamEvent) error) (*model.Response, error) {
		round++
		if round == 2 {
			if calls != 1 || len(r.Messages) != 3 {
				t.Fatal("missing tool result")
			}
			return streamReply(emit, textEvents("answer 42")...)
		}
		events := []msg.StreamEvent{{Type: msg.BlockStart, BlockID: "call", Block: msg.ToolUse("A", "work", []byte(`{`))}, {Type: msg.BlockDelta, BlockID: "call", Delta: `}`}, {Type: msg.BlockEnd, BlockID: "call"}}
		response, err := streamReply(emit, events...)
		if calls != 0 {
			t.Fatal("tool ran before Stream returned")
		}
		return response, err
	})
	a, _ := New(m, reg, 3)
	runner, _ := NewRunner(a)
	terminal := 0
	s, err := runner.Stream(context.Background(), RunRequest{Messages: input(), Hook: func(e Event) error {
		if e.Type == RunFinished {
			terminal++
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	events, result, err := collect(s)
	if err != nil || calls != 1 || terminal != 1 || result.Final.Text() != "answer 42" || len(events) != 6 {
		t.Fatalf("%+v %v", result, err)
	}
	for i, e := range events {
		if e.Sequence != i+1 || e.RunID != result.RunID {
			t.Fatal("identity")
		}
	}
	events[0].Event.Block.ToolUse.Name = "mutated"
	if result.History[1].Blocks[0].ToolUse.Name != "work" {
		t.Fatal("content aliases history")
	}
	if _, err = s.Recv(); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}
func TestStreamRejectsIncompleteFailedAndMismatchedMessages(t *testing.T) {
	for _, kind := range []string{"incomplete", "upstream", "mismatch", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			reg := registry(t, func(context.Context) ([]msg.Block, error) { calls++; return nil, nil })
			m := streamModel(func(ctx context.Context, r model.Request, emit func(msg.StreamEvent) error) (*model.Response, error) {
				if err := emit(msg.StreamEvent{Type: msg.BlockStart, BlockID: "c", Block: msg.ToolUse("A", "work", nil)}); err != nil {
					return nil, err
				}
				if kind == "upstream" {
					return nil, io.ErrUnexpectedEOF
				}
				delta := `{}`
				if kind == "invalid" {
					delta = `{`
				}
				if err := emit(msg.StreamEvent{Type: msg.BlockDelta, BlockID: "c", Delta: delta}); err != nil {
					return nil, err
				}
				if kind != "incomplete" {
					if err := emit(msg.StreamEvent{Type: msg.BlockEnd, BlockID: "c"}); err != nil {
						return nil, err
					}
				}
				return &model.Response{Message: msg.NewText("assistant", msg.RoleAssistant, "different")}, nil
			})
			a, _ := New(m, reg, 2)
			r, _ := NewRunner(a)
			s, _ := r.Stream(context.Background(), RunRequest{Messages: input()})
			defer s.Close()
			_, result, err := collect(s)
			if err == nil || calls != 0 || result.Final != nil || len(result.History) != 1 {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}
func TestStreamBackpressureCloseAndTimeout(t *testing.T) {
	for _, kind := range []string{"close", "model_timeout", "run_timeout"} {
		t.Run(kind, func(t *testing.T) {
			filled, exited := make(chan struct{}), make(chan struct{})
			m := streamModel(func(ctx context.Context, r model.Request, emit func(msg.StreamEvent) error) (*model.Response, error) {
				defer close(exited)
				if err := emit(msg.StreamEvent{Type: msg.BlockStart, BlockID: "t", Block: msg.Text("")}); err != nil {
					return nil, err
				}
				for i := 0; i < 15; i++ {
					if err := emit(msg.StreamEvent{Type: msg.BlockDelta, BlockID: "t", Delta: "x"}); err != nil {
						return nil, err
					}
				}
				close(filled)
				err := emit(msg.StreamEvent{Type: msg.BlockDelta, BlockID: "t", Delta: "blocked"})
				return nil, err
			})
			a, _ := New(m, registry(t, func(context.Context) ([]msg.Block, error) { return nil, nil }), 2)
			r, _ := NewRunner(a)
			req := RunRequest{Messages: input()}
			if kind == "model_timeout" {
				req.Timeouts.Model = 50 * time.Millisecond
			}
			if kind == "run_timeout" {
				req.Timeouts.Run = 50 * time.Millisecond
			}
			s, _ := r.Stream(context.Background(), req)
			defer s.Close()
			if kind == "close" {
				select {
				case <-filled:
				case <-time.After(time.Second):
					t.Fatal("not filled")
				}
				select {
				case <-exited:
					t.Fatal("queue did not block")
				default:
				}
				s.Close()
				s.Close()
			}
			select {
			case <-s.done:
			case <-time.After(time.Second):
				t.Fatal("worker leaked")
			}
			<-exited
			result, err := s.Wait()
			if result.StopReason != Canceled || err == nil {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}
func TestStreamSizeLimitAndUnsupported(t *testing.T) {
	m := streamModel(func(ctx context.Context, r model.Request, e func(msg.StreamEvent) error) (*model.Response, error) {
		return streamReply(e, textEvents(strings.Repeat("x", 1<<20))...)
	})
	a, _ := New(m, registry(t, func(context.Context) ([]msg.Block, error) { return nil, nil }), 1)
	r, _ := NewRunner(a)
	s, _ := r.Stream(context.Background(), RunRequest{Messages: input()})
	defer s.Close()
	_, _, err := collect(s)
	if err == nil {
		t.Fatal("no size limit")
	}
	plain, _ := NewRunner(strategyFunc(func(context.Context, ExecutionRequest) (*Result, error) { return &Result{}, nil }))
	if _, err := plain.Stream(context.Background(), RunRequest{Messages: input()}); !errors.Is(err, ErrStreamUnsupported) {
		t.Fatal(err)
	}
}

func TestStreamToolCancellationKeepsPartialResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	reg := registry(t, func(context.Context) ([]msg.Block, error) {
		calls++
		if calls == 2 {
			cancel()
			return nil, context.Canceled
		}
		return []msg.Block{msg.Text("42")}, nil
	})
	m := streamModel(func(ctx context.Context, r model.Request, emit func(msg.StreamEvent) error) (*model.Response, error) {
		var events []msg.StreamEvent
		for _, id := range []string{"A", "B", "C"} {
			events = append(events, msg.StreamEvent{Type: msg.BlockStart, BlockID: id, Block: msg.ToolUse(id, "work", nil)}, msg.StreamEvent{Type: msg.BlockDelta, BlockID: id, Delta: `{}`}, msg.StreamEvent{Type: msg.BlockEnd, BlockID: id})
		}
		return streamReply(emit, events...)
	})
	a, _ := New(m, reg, 3)
	r, _ := NewRunner(a)
	s, _ := r.Stream(ctx, RunRequest{Messages: input()})
	defer s.Close()
	_, result, err := collect(s)
	if !errors.Is(err, context.Canceled) || calls != 2 || len(result.ToolBatches) != 1 || len(result.History) != 3 {
		t.Fatalf("%+v %v", result, err)
	}
	batch := result.ToolBatches[0]
	for i, want := range []string{"succeeded", "unknown", "not_started"} {
		if string(batch.Calls[i].Outcome) != want {
			t.Fatal(batch.Calls)
		}
	}
}
