package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/session"
)

// SessionRequest identifies an existing session. Store lifetime belongs to caller.
// SaveTimeout defaults to 3s and applies even after run cancellation.
type SessionRequest struct {
	Store       session.Store
	Key         session.Key
	SaveTimeout time.Duration
}

func (r *Runner) RunSession(ctx context.Context, binding SessionRequest, req RunRequest) (*Result, error) {
	req.Session = &binding
	return r.Run(ctx, req)
}
func (r *Runner) StreamSession(ctx context.Context, binding SessionRequest, req RunRequest) (*Stream, error) {
	req.Session = &binding
	return r.Stream(ctx, req)
}

func loadSession(ctx context.Context, binding *SessionRequest, incoming []*msg.Msg) (*session.Snapshot, []*msg.Msg, error) {
	if binding.Store == nil || binding.SaveTimeout < 0 {
		return nil, nil, fmt.Errorf("store and nonnegative save timeout required")
	}
	for _, m := range incoming {
		if m.Role != msg.RoleUser {
			return nil, nil, fmt.Errorf("session input must contain new user messages only")
		}
	}
	snapshot, err := binding.Store.Load(ctx, binding.Key)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err = session.Clone(snapshot)
	if err != nil {
		return nil, nil, err
	}
	if snapshot.State != session.Ready {
		return nil, nil, session.ErrBlocked
	}
	messages := append(snapshot.History, incoming...)
	return snapshot, messages, nil
}

func commitSession(ctx context.Context, binding *SessionRequest, loaded *session.Snapshot, input []*msg.Msg, result *Result, runErr error) error {
	candidate, err := session.Clone(loaded)
	if err != nil {
		return err
	}
	history := result.History
	if len(history) == 0 {
		history = input
		if runErr == nil && result.Final != nil {
			history = append(append([]*msg.Msg(nil), input...), result.Final)
		}
	}
	// A custom strategy may not rewrite the input prefix when using sessions.
	if len(history) < len(input) {
		return fmt.Errorf("strategy returned shortened session history")
	}
	before, err := json.Marshal(input)
	if err != nil {
		return err
	}
	after, err := json.Marshal(history[:len(input)])
	if err != nil {
		return err
	}
	if !bytes.Equal(before, after) {
		return fmt.Errorf("strategy rewrote session input")
	}
	candidate.History = history
	candidate.LastRunID = result.RunID
	candidate.StopReason = string(result.StopReason)
	candidate.ToolBatches = result.ToolBatches
	candidate.State = session.Ready
	// Conservative: failed runs need explicit resolution, even if all calls pair.
	if runErr != nil || msg.ValidateConversation(history, true) != nil {
		candidate.State = session.Blocked
	}
	if err = candidate.Validate(); err != nil {
		return err
	}
	timeout := binding.SaveTimeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	saved, err := binding.Store.Save(cleanup, candidate)
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	result.SessionSaved = true
	result.SessionVersion = saved.Version
	return nil
}
