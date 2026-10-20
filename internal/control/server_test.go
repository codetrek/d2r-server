package control

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func TestRunRejectsInvalidConfigAndPreservesCancellation(t *testing.T) {
	for _, config := range []Config{{ShutdownTimeout: time.Second}, {ListenAddress: "127.0.0.1:0"}, {ListenAddress: "127.0.0.1:0", ShutdownTimeout: -time.Second}} {
		if err := Run(context.Background(), config, quietLogger()); err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, Config{ListenAddress: "127.0.0.1:0", ShutdownTimeout: time.Second}, quietLogger()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestListenFailureRetainsNetworkError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	err = Run(context.Background(), Config{ListenAddress: listener.Addr().String(), ShutdownTimeout: time.Second}, quietLogger())
	var networkError *net.OpError
	if !errors.As(err, &networkError) || networkError.Op != "listen" {
		t.Fatalf("listen error chain lost: %v", err)
	}
}

func TestHTTPResourceLimits(t *testing.T) {
	server := serverFor(Handler(quietLogger()), quietLogger())
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 10*time.Second || server.WriteTimeout != 10*time.Second || server.IdleTimeout != 60*time.Second || server.MaxHeaderBytes != 16<<10 {
		t.Fatal("HTTP resource limits changed")
	}
}

type contextKey struct{}

func TestShutdownDrainsActiveRequestAndPreservesContext(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "preserved"))
	defer cancel()
	entered, release := make(chan bool, 1), make(chan struct{})
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		entered <- request.Context().Value(contextKey{}) == "preserved"
		<-release
		if request.Context().Err() != nil {
			writer.WriteHeader(500)
			return
		}
		writer.WriteHeader(204)
	})
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listener, time.Second, quietLogger(), handler) }()
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	responseDone := make(chan error, 1)
	go func() {
		response, err := client.Get("http://" + listener.Addr().String() + "/healthz")
		if err == nil {
			defer response.Body.Close()
			if response.StatusCode != 204 {
				err = errors.New("request was interrupted")
			}
		}
		responseDone <- err
	}()
	select {
	case preserved := <-entered:
		if !preserved {
			t.Fatal("request context value lost")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("server stopped before active request drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-responseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not finish")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unexpected drain result: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server goroutine did not stop")
	}
	assertListenerClosed(t, listener.Addr().String())
}

func TestShutdownDeadlineForceClosesActiveRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, handlerDone := make(chan struct{}), make(chan struct{})
	handler := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(entered)
		<-request.Context().Done()
		close(handlerDone)
	})
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listener, 20*time.Millisecond, quietLogger(), handler) }()
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	clientDone := make(chan struct{})
	go func() {
		response, _ := client.Get("http://" + listener.Addr().String() + "/healthz")
		if response != nil {
			response.Body.Close()
		}
		close(clientDone)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown causes lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not force-close")
	}
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("active handler did not observe connection close")
	}
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatal("client goroutine did not finish")
	}
	assertListenerClosed(t, listener.Addr().String())
}

func TestServeFailureIsReported(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	err = serve(context.Background(), listener, time.Second, quietLogger(), Handler(quietLogger()))
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("serve failure lost: %v", err)
	}
}

func assertListenerClosed(t *testing.T, address string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("owned listener remained open")
	}
}
