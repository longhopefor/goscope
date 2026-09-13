package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/longhopefor/goscope/model"
	"github.com/longhopefor/goscope/msg"
)

func TestProgressOrderAndHookIsolation(t *testing.T) {
	reg := registry(t, func(context.Context) ([]msg.Block, error) { return []msg.Block{msg.Text("42")}, nil })
	n := 0
	m := modelFunc(func(context.Context, model.Request) (*model.Response, error) {
		n++
		if n == 1 {
			return response(msg.ToolUse("1", "work", json.RawMessage(`{}`)), msg.ToolUse("2", "missing", json.RawMessage(`{}`))), nil
		}
		return response(msg.Text("done")), nil
	})
	a, _ := New(m, reg, 3)
	var events []Event
	r, err := a.RunWithRequest(context.Background(), RunRequest{Messages: input(), Hook: func(e Event) error {
		events = append(events, e)
		e.ToolName = "changed"
		e.RunID = "changed"
		if e.Type == ToolStarted {
			panic("hook panic")
		}
		return errors.New("observer unavailable")
	}})
	if err != nil || r.StopReason != Completed || r.Final.Text() != "done" || r.HookFailures != len(events) {
		t.Fatalf("%+v %v", r, err)
	}
	want := []EventType{RunStarted, ModelStarted, ModelFinished, ToolStarted, ToolFinished, ToolStarted, ToolFinished, ModelStarted, ModelFinished, RunFinished}
	var got []EventType
	for i, e := range events {
		got = append(got, e.Type)
		if e.Sequence != i+1 || e.RunID != r.RunID || e.Time.IsZero() {
			t.Fatal("event identity")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if events[4].Status != "succeeded" || events[6].Status != "failed" || events[9].StopReason != r.StopReason {
		t.Fatal("wrong status")
	}
}

func TestProgressTerminalPaths(t *testing.T) {
	for _, kind := range []string{"limit", "model_error", "invalid", "before", "model_start", "tool_start", "tool_finish"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			executions, models := 0, 0
			reg := registry(t, func(context.Context) ([]msg.Block, error) { executions++; return []msg.Block{msg.Text("ok")}, nil })
			m := modelFunc(func(context.Context, model.Request) (*model.Response, error) {
				models++
				if kind == "model_error" {
					return nil, errors.New("bad model")
				}
				return response(msg.ToolUse("1", "work", json.RawMessage(`{}`)), msg.ToolUse("2", "work", json.RawMessage(`{}`))), nil
			})
			a, _ := New(m, reg, 1)
			history := input()
			if kind == "invalid" {
				history = nil
			}
			if kind == "before" {
				cancel()
			}
			var events []Event
			r, err := a.RunWithRequest(ctx, RunRequest{Messages: history, Hook: func(e Event) error {
				events = append(events, e)
				if kind == "model_start" && e.Type == ModelStarted || kind == "tool_start" && e.Type == ToolStarted || kind == "tool_finish" && e.Type == ToolFinished {
					cancel()
				}
				return nil
			}})
			if err == nil {
				t.Fatal("expected error")
			}
			terminal := 0
			for _, e := range events {
				if e.Type == RunFinished {
					terminal++
					if e.StopReason != r.StopReason || e.Step != r.Steps {
						t.Fatal("terminal mismatch")
					}
				}
			}
			if terminal != 1 || events[0].Type != RunStarted || events[len(events)-1].Type != RunFinished {
				t.Fatal("terminal order")
			}
			switch kind {
			case "limit":
				if r.StopReason != MaxSteps || executions != 2 {
					t.Fatal("limit")
				}
			case "model_error", "invalid":
				if r.StopReason != Failed || executions != 0 {
					t.Fatal("failed")
				}
			default:
				if r.StopReason != Canceled || !errors.Is(err, context.Canceled) {
					t.Fatal("canceled")
				}
			}
			if kind == "model_start" && (models != 0 || r.Steps != 0 || events[1].Step != events[2].Step) {
				t.Fatal("model started after cancellation")
			}
			if kind == "tool_start" && executions != 0 || kind == "tool_finish" && executions != 1 {
				t.Fatal("tool continued")
			}
		})
	}
}
