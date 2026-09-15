package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/session"
)

func newSession(t *testing.T) (*session.Memory, SessionRequest) {
	t.Helper()
	s := session.NewMemory()
	key := session.Key{Namespace: "test", ID: "chat"}
	if _, err := s.Save(context.Background(), session.New(key)); err != nil {
		t.Fatal(err)
	}
	return s, SessionRequest{Store: s, Key: key}
}
func TestSessionContinuesAndSavesBeforeTerminal(t *testing.T) {
	store, binding := newSession(t)
	n := 0
	runner, _ := NewRunner(strategyFunc(func(ctx context.Context, req ExecutionRequest) (*Result, error) {
		n++
		if len(req.Messages) != (n-1)*2+1 {
			t.Fatal("history not loaded")
		}
		return &Result{Final: msg.NewText("assistant", msg.RoleAssistant, "answer")}, nil
	}))
	var firstID string
	for i := 0; i < 2; i++ {
		r, err := runner.RunSession(context.Background(), binding, RunRequest{Messages: input(), Hook: func(e Event) error {
			if e.Type == RunFinished {
				saved, err := store.Load(context.Background(), binding.Key)
				if err != nil || saved.Version != int64(i+2) {
					t.Error("terminal before persistence")
				}
			}
			return nil
		}})
		if err != nil || !r.SessionSaved || r.SessionVersion != int64(i+2) {
			t.Fatalf("%+v %v", r, err)
		}
		if i == 0 {
			firstID = r.RunID
		} else if r.RunID == firstID {
			t.Fatal("reused run ID")
		}
	}
}

type failingStore struct {
	session.Store
	err error
}

func (s failingStore) Save(context.Context, *session.Snapshot) (*session.Snapshot, error) {
	return nil, s.err
}
func TestSessionSaveFailureDoesNotRetry(t *testing.T) {
	store, binding := newSession(t)
	sentinel := errors.New("disk failure")
	binding.Store = failingStore{store, sentinel}
	calls := 0
	runner, _ := NewRunner(strategyFunc(func(context.Context, ExecutionRequest) (*Result, error) {
		calls++
		return &Result{Final: msg.NewText("assistant", msg.RoleAssistant, "useful answer")}, nil
	}))
	result, err := runner.RunSession(context.Background(), binding, RunRequest{Messages: input()})
	if !errors.Is(err, sentinel) || calls != 1 || result.SessionSaved || result.Final == nil || result.StopReason != Failed {
		t.Fatalf("%+v %v", result, err)
	}
	loaded, _ := store.Load(context.Background(), binding.Key)
	if loaded.Version != 1 {
		t.Fatal("changed on failure")
	}
}
func TestSessionConflictDoesNotRetryExecution(t *testing.T) {
	store, binding := newSession(t)
	calls := 0
	runner, _ := NewRunner(strategyFunc(func(context.Context, ExecutionRequest) (*Result, error) {
		calls++
		other, _ := store.Load(context.Background(), binding.Key)
		store.Save(context.Background(), other)
		return &Result{Final: msg.NewText("assistant", msg.RoleAssistant, "answer")}, nil
	}))
	result, err := runner.RunSession(context.Background(), binding, RunRequest{Messages: input()})
	if !errors.Is(err, session.ErrConflict) || calls != 1 || result.SessionSaved {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestCanceledSessionPersistsDiagnosticAndBlocksContinuation(t *testing.T) {
	store, binding := newSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := modelFunc(func(context.Context, model.Request) (*model.Response, error) {
		return response(msg.ToolUse("A", "work", []byte(`{}`)), msg.ToolUse("B", "work", []byte(`{}`)), msg.ToolUse("C", "work", []byte(`{}`))), nil
	})
	calls := 0
	reg := registry(t, func(context.Context) ([]msg.Block, error) {
		calls++
		if calls == 2 {
			cancel()
			return nil, context.Canceled
		}
		return []msg.Block{msg.Text("ok")}, nil
	})
	react, _ := New(m, reg, 2)
	runner, _ := NewRunner(react)
	result, err := runner.RunSession(ctx, binding, RunRequest{Messages: input()})
	if !errors.Is(err, context.Canceled) || !result.SessionSaved {
		t.Fatalf("%+v %v", result, err)
	}
	saved, _ := store.Load(context.Background(), binding.Key)
	if saved.State != session.Blocked || saved.LastRunID != result.RunID || len(saved.ToolBatches) != 1 || len(saved.History) != 3 {
		t.Fatalf("%+v", saved)
	}
	if _, err = runner.RunSession(context.Background(), binding, RunRequest{Messages: input()}); !errors.Is(err, session.ErrBlocked) || calls != 2 {
		t.Fatal(err, calls)
	}
}

type timeoutStore struct{ session.Store }

func (s timeoutStore) Save(ctx context.Context, _ *session.Snapshot) (*session.Snapshot, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestSessionSaveDeadline(t *testing.T) {
	store, binding := newSession(t)
	binding.Store = timeoutStore{store}
	binding.SaveTimeout = 10 * time.Millisecond
	runner, _ := NewRunner(strategyFunc(func(context.Context, ExecutionRequest) (*Result, error) {
		return &Result{Final: msg.NewText("assistant", msg.RoleAssistant, "answer")}, nil
	}))
	r, err := runner.RunSession(context.Background(), binding, RunRequest{Messages: input()})
	if !errors.Is(err, context.DeadlineExceeded) || r.SessionSaved {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestStreamingSessionPersists(t *testing.T) {
	store, binding := newSession(t)
	m := streamModel(func(ctx context.Context, r model.Request, e func(msg.StreamEvent) error) (*model.Response, error) {
		return streamReply(e, textEvents("hello")...)
	})
	react, _ := New(m, registry(t, func(context.Context) ([]msg.Block, error) { return nil, nil }), 2)
	runner, _ := NewRunner(react)
	s, err := runner.StreamSession(context.Background(), binding, RunRequest{Messages: input()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, r, err := collect(s)
	if err != nil || !r.SessionSaved {
		t.Fatalf("%+v %v", r, err)
	}
	loaded, _ := store.Load(context.Background(), binding.Key)
	if len(loaded.History) != 2 {
		t.Fatal(loaded)
	}
}
