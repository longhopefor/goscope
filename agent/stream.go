package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/longhopefor/goscope/msg"
)

var ErrStreamUnsupported = errors.New("streaming is not supported")

// StreamingAgent is optional. Implementations must stop when consume returns an
// error, invoke it synchronously, and join their work before returning.
type StreamingAgent interface {
	ExecuteStream(context.Context, ExecutionRequest, func(context.Context, ContentEvent) error) (*Result, error)
}

// ContentEvent is provisional content, separate from lifecycle Hook events.
// BlockID is scoped to Step, not the entire run. Sequence counts content only.
type ContentEvent struct {
	RunID    string
	Sequence int
	Step     int
	Event    msg.StreamEvent
}

// Stream has one reader. Close requests cancellation and joins execution.
// Drain Recv before Wait, or Close first. Wait alone does not drain the queue.
// Result ownership transfers through Wait; repeated Wait returns the same pointer.
// Do not call Close/Wait from a Hook executing on this stream's worker.
type Stream struct {
	events chan ContentEvent
	done   chan struct{}
	cancel context.CancelFunc
	result *Result
	err    error
}

// Stream starts one owned worker with a bounded 16-event queue. A slow reader
// applies backpressure; parent cancellation or Close unblocks queue sends.
// Each event is limited to 1 MiB of its internal JSON envelope; this is not a total run budget.
// Stop reading early only after arranging Close, normally with defer.
func (r *Runner) Stream(ctx context.Context, req RunRequest) (*Stream, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if r == nil || nilAgent(r.agent) {
		return nil, fmt.Errorf("runner is required")
	}
	if _, ok := r.agent.(StreamingAgent); !ok {
		return nil, ErrStreamUnsupported
	}
	// Snapshot before launching a worker so callers may reuse their request.
	messages, err := clone(req.Messages)
	if err != nil {
		return nil, err
	}
	req.Messages = messages
	child, cancel := context.WithCancel(ctx)
	s := &Stream{events: make(chan ContentEvent, 16), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(s.done)
		defer close(s.events)
		defer cancel()
		s.result, s.err = r.run(child, req, func(callCtx context.Context, e ContentEvent) error {
			if err := callCtx.Err(); err != nil {
				return err
			}
			select {
			case <-child.Done():
				return child.Err()
			case <-callCtx.Done():
				return callCtx.Err()
			case s.events <- e:
				return nil
			}
		})
	}()
	return s, nil
}

func (s *Stream) Recv() (ContentEvent, error) {
	e, ok := <-s.events
	if ok {
		return e, nil
	}
	<-s.done
	if s.err != nil {
		return ContentEvent{}, s.err
	}
	return ContentEvent{}, io.EOF
}
func (s *Stream) Wait() (*Result, error) { <-s.done; return s.result, s.err }
func (s *Stream) Close() error {
	// Do not change a completed run into a canceled one.
	select {
	case <-s.done:
		return nil
	default:
	}
	s.cancel()
	<-s.done
	return nil
}

func cloneContent(e ContentEvent) (ContentEvent, error) {
	// Tool starts may carry incomplete JSON; encode its bytes separately.
	var input []byte
	if e.Event.Block.ToolUse != nil {
		v := *e.Event.Block.ToolUse
		input = append([]byte(nil), v.Input...)
		v.Input = nil
		e.Event.Block.ToolUse = &v
	}
	raw, err := json.Marshal(struct {
		Content ContentEvent
		Input   []byte
	}{e, input})
	if err != nil {
		return ContentEvent{}, err
	}
	if len(raw) > 1<<20 {
		return ContentEvent{}, fmt.Errorf("content event exceeds 1 MiB")
	}
	var copied struct {
		Content ContentEvent
		Input   []byte
	}
	if err = json.Unmarshal(raw, &copied); err != nil {
		return ContentEvent{}, err
	}
	if copied.Content.Event.Block.ToolUse != nil {
		copied.Content.Event.Block.ToolUse.Input = copied.Input
	}
	return copied.Content, nil
}
