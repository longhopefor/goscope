package tool

import (
	"context"
	"errors"
	"github.com/longhopefor/goscope/msg"
	"testing"
	"time"
)

func TestBatchCancellationRetainsFacts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sideEffect, third := false, false
	a, _ := New("a", "a", func(context.Context, struct{}) ([]msg.Block, error) { return []msg.Block{msg.Text("done")}, nil })
	b, _ := New("b", "b", func(context.Context, struct{}) ([]msg.Block, error) {
		sideEffect = true
		cancel()
		return nil, context.Canceled
	})
	c, _ := New("c", "c", func(context.Context, struct{}) ([]msg.Block, error) { third = true; return nil, nil })
	r, _ := NewRegistry(a, b, c)
	req := msg.New("assistant", msg.RoleAssistant, msg.ToolUse("a", "a", []byte(`{}`)), msg.ToolUse("b", "b", []byte(`{}`)), msg.ToolUse("c", "c", []byte(`{}`)))
	batch, err := r.ExecuteBatch(ctx, req, BatchOptions{})
	if !errors.Is(err, context.Canceled) || !sideEffect || third {
		t.Fatalf("err=%v sideEffect=%v third=%v", err, sideEffect, third)
	}
	for i, want := range []Outcome{Succeeded, Unknown, NotStarted} {
		if batch.Calls[i].Outcome != want {
			t.Fatalf("calls=%+v", batch.Calls)
		}
	}
	if batch.Calls[0].AttemptID == "" || batch.Calls[1].AttemptID == "" || batch.Calls[2].AttemptID != "" {
		t.Fatal(batch.Calls)
	}
	if batch.Message == nil || len(batch.Message.Blocks) != 1 {
		t.Fatal("lost confirmed result")
	}
}

func TestBatchTimeoutAndCleanup(t *testing.T) {
	var saved context.Context
	exited := false
	b, _ := New("b", "b", func(ctx context.Context, _ struct{}) ([]msg.Block, error) {
		saved = ctx
		defer func() { exited = true }()
		<-ctx.Done()
		return nil, ctx.Err()
	})
	r, _ := NewRegistry(b)
	req := msg.New("assistant", msg.RoleAssistant, msg.ToolUse("b", "b", []byte(`{}`)))
	batch, err := r.ExecuteBatch(context.Background(), req, BatchOptions{Timeout: 10 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || !exited || saved.Err() == nil || batch.Calls[0].Outcome != Unknown {
		t.Fatalf("%+v %v", batch, err)
	}
}

func TestBatchSuccessReleasesChildAndObserverCancellation(t *testing.T) {
	var saved context.Context
	b, _ := New("b", "b", func(ctx context.Context, _ struct{}) ([]msg.Block, error) {
		saved = ctx
		return []msg.Block{msg.Text("ok")}, nil
	})
	r, _ := NewRegistry(b)
	req := msg.New("assistant", msg.RoleAssistant, msg.ToolUse("b", "b", []byte(`{}`)), msg.ToolUse("c", "b", []byte(`{}`)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	batch, err := r.ExecuteBatch(ctx, req, BatchOptions{Observer: func(p Progress) {
		if p.Finished {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) || saved.Err() == nil || batch.Calls[0].Outcome != Succeeded || batch.Calls[1].Outcome != NotStarted {
		t.Fatalf("%+v %v", batch, err)
	}
}

func TestBatchCancelBeforeInvocation(t *testing.T) {
	for _, before := range []bool{true, false} {
		invoked := false
		b, _ := New("b", "b", func(context.Context, struct{}) ([]msg.Block, error) { invoked = true; return nil, nil })
		r, _ := NewRegistry(b)
		ctx, cancel := context.WithCancel(context.Background())
		if before {
			cancel()
		}
		batch, err := r.ExecuteBatch(ctx, msg.New("assistant", msg.RoleAssistant, msg.ToolUse("b", "b", []byte(`{}`))), BatchOptions{Observer: func(p Progress) {
			if !p.Finished {
				cancel()
			}
		}})
		cancel()
		if !errors.Is(err, context.Canceled) || invoked || batch.Calls[0].Outcome != NotStarted || batch.Calls[0].AttemptID != "" {
			t.Fatalf("%+v %v", batch, err)
		}
	}
}
