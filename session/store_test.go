package session_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/session"
	"github.com/longhopefor/goscope/session/sqlite"
)

func TestStoreContract(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var store session.Store = session.NewMemory()
			if backend == "sqlite" {
				s, err := sqlite.Open(filepath.Join(t.TempDir(), "sessions.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				store = s
			}
			ctx := context.Background()
			key := session.Key{Namespace: "tenant-a", ID: "same-id"}
			if _, err := store.Load(ctx, key); !errors.Is(err, session.ErrNotFound) {
				t.Fatal(err)
			}
			initial := session.New(key)
			initial.History = []*msg.Msg{msg.NewText("user", msg.RoleUser, "hello")}
			saved, err := store.Save(ctx, initial)
			if err != nil || saved.Version != 1 || initial.Version != 0 {
				t.Fatalf("%+v %v", saved, err)
			}
			saved.History[0].Blocks[0].Text.Text = "mutated"
			current, err := store.Load(ctx, key)
			if err != nil || current.History[0].Text() != "hello" {
				t.Fatal("aliased input/output")
			}
			if _, err := store.Save(ctx, initial); !errors.Is(err, session.ErrConflict) {
				t.Fatal(err)
			}
			other := session.New(session.Key{Namespace: "tenant-b", ID: key.ID})
			if _, err := store.Save(ctx, other); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for i := 0; i < 2; i++ {
				copy, _ := session.Clone(current)
				wg.Add(1)
				go func() { defer wg.Done(); _, err := store.Save(ctx, copy); results <- err }()
			}
			wg.Wait()
			close(results)
			successes, conflicts := 0, 0
			for err := range results {
				if err == nil {
					successes++
				} else if errors.Is(err, session.ErrConflict) {
					conflicts++
				} else {
					t.Fatal(err)
				}
			}
			if successes != 1 || conflicts != 1 {
				t.Fatal(successes, conflicts)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := store.Save(canceled, other); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			invalid := session.New(session.Key{Namespace: "x", ID: "invalid"})
			invalid.History = []*msg.Msg{msg.New("assistant", msg.RoleAssistant, msg.ToolUse("call", "tool", []byte(`{}`)))}
			if _, err := store.Save(ctx, invalid); err == nil {
				t.Fatal("ready unresolved accepted")
			}
			invalid.State = session.Blocked
			if _, err := store.Save(ctx, invalid); err != nil {
				t.Fatal(err)
			}
			invalid.FormatVersion = 2
			if _, err := store.Save(ctx, invalid); err == nil {
				t.Fatal("future schema accepted")
			}
		})
	}
}
func TestSQLiteReopenAndIndependentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	a, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	key := session.Key{Namespace: "app", ID: "chat"}
	first, err := a.Save(context.Background(), session.New(key))
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	a, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	loaded, err := a.Load(context.Background(), key)
	if err != nil || loaded.Version != first.Version {
		t.Fatalf("%+v %v", loaded, err)
	}
	b, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, s := range []*sqlite.Store{a, b} {
		copy, _ := session.Clone(loaded)
		wg.Add(1)
		go func(s *sqlite.Store) { defer wg.Done(); _, err := s.Save(context.Background(), copy); results <- err }(s)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, session.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
}
