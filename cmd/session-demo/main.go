// session-demo persists a conversation across process invocations without an API key.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/longhopefor/goscope/agent"
	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/session"
	"github.com/longhopefor/goscope/session/sqlite"
	"github.com/longhopefor/goscope/tool"
)

type scripted struct{}

func (scripted) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	count := 0
	first := ""
	for _, m := range req.Messages {
		if m.Role == msg.RoleUser {
			count++
			if first == "" {
				first = m.Text()
			}
		}
	}
	return &model.Response{Message: msg.NewText("assistant", msg.RoleAssistant, fmt.Sprintf("turn=%d first=%q", count, first))}, nil
}
func run() error {
	path := flag.String("db", "sessions.db", "SQLite file")
	id := flag.String("id", "chat-1", "session ID")
	namespace := flag.String("namespace", "demo", "session namespace")
	text := flag.String("message", "hello", "new user message")
	flag.Parse()
	store, err := sqlite.Open(*path)
	if err != nil {
		return err
	}
	defer store.Close()
	key := session.Key{Namespace: *namespace, ID: *id}
	ctx := context.Background()
	loaded, err := store.Load(ctx, key)
	if errors.Is(err, session.ErrNotFound) {
		loaded, err = store.Save(ctx, session.New(key))
	}
	if err != nil {
		return err
	}
	fmt.Printf("loaded version=%d messages=%d\n", loaded.Version, len(loaded.History))
	registry, err := tool.NewRegistry()
	if err != nil {
		return err
	}
	react, err := agent.New(scripted{}, registry, 3)
	if err != nil {
		return err
	}
	runner, err := agent.NewRunner(react)
	if err != nil {
		return err
	}
	result, err := runner.RunSession(ctx, agent.SessionRequest{Store: store, Key: key}, agent.RunRequest{Messages: []*msg.Msg{msg.NewText("user", msg.RoleUser, *text)}})
	if err != nil {
		return err
	}
	fmt.Printf("saved=%v version=%d answer=%s\n", result.SessionSaved, result.SessionVersion, result.Final.Text())
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
