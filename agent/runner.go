package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/longhopefor/goscope/msg"
)

// Agent executes a strategy, not the run lifecycle. Implementations must honor
// ctx, return partial results on errors, and stop work before returning.
// Emit is synchronous; do not call it recursively from a Hook.
// A returned Result transfers ownership to the caller.
type Agent interface {
	Execute(context.Context, ExecutionRequest) (*Result, error)
}

// ExecutionRequest contains strategy inputs. Run deadlines arrive through ctx.
// Emit accepts only model/tool events; Runner owns run events and metadata.
type ExecutionRequest struct {
	Messages     []*msg.Msg
	ModelTimeout time.Duration
	ToolTimeout  time.Duration
	Emit         func(Event)
}

// Runner has no per-run mutable state. Concurrent runs require a concurrent-safe Agent.
type Runner struct{ agent Agent }

func NewRunner(a Agent) (*Runner, error) {
	if nilAgent(a) {
		return nil, fmt.Errorf("agent is required")
	}
	return &Runner{agent: a}, nil
}
func nilAgent(a Agent) bool {
	if a == nil {
		return true
	}
	v := reflect.ValueOf(a)
	switch v.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Func, reflect.Interface, reflect.Slice, reflect.Chan:
		return v.IsNil()
	}
	return false
}

// Run always returns a result. Hook failures cannot change execution outcomes.
// Cancellation is cooperative; there is no background timeout goroutine.
func (runner *Runner) Run(ctx context.Context, req RunRequest) (result *Result, err error) {
	return runner.run(ctx, req, nil)
}

func (runner *Runner) run(ctx context.Context, req RunRequest, content func(context.Context, ContentEvent) error) (result *Result, err error) {
	started := time.Now()
	runID := msg.NewID()
	result = &Result{StopReason: Failed}
	var mu sync.Mutex
	sequence, failures := 0, 0
	closed := false
	publish := func(e Event) {
		sequence++
		e.RunID, e.Sequence, e.Time = runID, sequence, time.Now()
		if notify(req.Hook, e) != nil {
			failures++
		}
	}
	publish(Event{Type: RunStarted, Status: "started"})
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("agent panicked")
			result = &Result{StopReason: Failed}
		}
		if result == nil {
			result = &Result{}
			if err == nil {
				err = fmt.Errorf("agent returned no result")
			}
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.StopReason = Canceled
		} else if errors.Is(err, ErrMaxSteps) {
			result.StopReason = MaxSteps
		} else if err != nil {
			result.StopReason = Failed
		} else {
			result.StopReason = Completed
		}
		if err != nil {
			result.Final = nil
		}
		mu.Lock()
		defer mu.Unlock()
		closed = true
		publish(Event{Type: RunFinished, Step: result.Steps, StopReason: result.StopReason})
		result.RunID, result.HookFailures = runID, failures
	}()
	if runner == nil || nilAgent(runner.agent) || ctx == nil {
		return result, fmt.Errorf("runner and context are required")
	}
	if req.Timeouts.Run < 0 || req.Timeouts.Model < 0 || req.Timeouts.Tool < 0 {
		return result, fmt.Errorf("timeouts must be nonnegative")
	}
	var cancel context.CancelFunc
	if req.Timeouts.Run > 0 {
		ctx, cancel = context.WithDeadline(ctx, started.Add(req.Timeouts.Run))
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if len(req.Messages) == 0 {
		return result, fmt.Errorf("empty history")
	}
	messages, copyErr := clone(req.Messages)
	if copyErr != nil {
		return result, copyErr
	}
	if err = msg.ValidateConversation(messages, true); err != nil {
		return result, err
	}
	emit := func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		switch e.Type {
		case ModelStarted, ModelFinished, ToolStarted, ToolFinished:
		default:
			return
		}
		e.StopReason = ""
		publish(e)
	}
	execution := ExecutionRequest{Messages: messages, ModelTimeout: req.Timeouts.Model, ToolTimeout: req.Timeouts.Tool, Emit: emit}
	if content == nil {
		result, err = runner.agent.Execute(ctx, execution)
	} else {
		strategy, ok := runner.agent.(StreamingAgent)
		if !ok {
			return result, ErrStreamUnsupported
		}
		contentSequence := 0
		result, err = strategy.ExecuteStream(ctx, execution, func(callCtx context.Context, e ContentEvent) error {
			contentSequence++
			e.RunID, e.Sequence = runID, contentSequence
			copied, copyErr := cloneContent(e)
			if copyErr != nil {
				return copyErr
			}
			return content(callCtx, copied)
		})
	}
	// Preserve the strategy error as well when cancellation races with its return.
	if contextErr := ctx.Err(); contextErr != nil {
		err = errors.Join(err, contextErr)
	}
	return result, err
}
