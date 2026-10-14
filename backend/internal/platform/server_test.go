package platform

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRunDrainsRequestsAndClosesListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
			_, _ = io.WriteString(w, "finished")
		}), time.Second)
	}()
	client := &http.Client{Timeout: 3 * time.Second}
	response := make(chan string, 1)
	go func() {
		r, e := client.Get("http://" + listener.Addr().String())
		if e != nil {
			response <- "failed"
			return
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		response <- string(b)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("returned before request drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if body := <-response; body != "finished" {
		t.Fatalf("request interrupted: %s", body)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if r, err := client.Get("http://" + listener.Addr().String()); err == nil {
		r.Body.Close()
		t.Fatal("listener remained open")
	}
}
