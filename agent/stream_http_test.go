package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/longhopefor/goscope/model/openai"
	"github.com/longhopefor/goscope/msg"
)

func TestStreamCloseCancelsHTTPBody(t *testing.T) {
	disconnected := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(disconnected)
	}))
	defer server.Close()
	client, err := openai.New(openai.Config{APIKey: "test", Model: "test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := New(client, registry(t, func(context.Context) ([]msg.Block, error) { return nil, nil }), 2)
	runner, _ := NewRunner(a)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := runner.Stream(ctx, RunRequest{Messages: input()})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err = stream.Recv(); err != nil {
		t.Fatal(err)
	}
	stream.Close()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("HTTP request still running")
	}
	result, err := stream.Wait()
	if err == nil || result.StopReason != Canceled {
		t.Fatalf("%+v %v", result, err)
	}
}
