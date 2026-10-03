//go:build windows

package adminpipe

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type pendingPipeHandler struct {
	called  chan struct{}
	release <-chan struct{}
}

func (h pendingPipeHandler) Execute(_ context.Context, request Request) (Result, *Error) {
	if h.called != nil {
		close(h.called)
	}
	if h.release != nil {
		<-h.release
	}
	return Result{Operation: request.Operation, Model: request.ModelID, Status: "completed"}, nil
}

func TestRegressionPipeExecuteHonorsExchangeTimeout(t *testing.T) {
	name := testPipeName(t, "senior-timeout")
	called := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	startListener(t, name, pendingPipeHandler{release: release, called: called})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(DialOptions{Name: name, Timeout: 80 * time.Millisecond, ExpectedServerPath: executable})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := client.Execute(context.Background(), Request{Operation: OperationLoad, ModelID: "local-coding"})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("exchange timeout error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("exchange did not time out while the handler was still waiting")
	}
	select {
	case <-called:
	default:
		t.Fatal("timeout occurred before the request reached the handler")
	}
}

func TestRegressionPipeServeStopsWhenPeerDoesNotRead(t *testing.T) {
	name := testPipeName(t, "senior-no-read")
	listener, err := Listen(name)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called, stopped := make(chan struct{}), make(chan struct{})
	go func() { defer close(stopped); _ = listener.Serve(ctx, pendingPipeHandler{called: called}, nil) }()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := Dial(context.Background(), DialOptions{Name: name, Timeout: time.Second, ExpectedServerPath: executable})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("{\"operation\":\"load\",\"model_id\":\"local-coding\"}\n")); err != nil {
		t.Fatal(err)
	}
	<-called
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-stopped:
	case <-time.After(500 * time.Millisecond):
		t.Error("canceled Serve remained blocked because peer did not read the reply")
	}
	// Release the disposable peer even when a regression prevents shutdown.
	_ = conn.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("pipe fixture did not release after client close")
	}
}

func TestPipeClientCancellationInterruptsAnAdmittedExchange(t *testing.T) {
	name := testPipeName(t, "cancel-exchange")
	called := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	startListener(t, name, pendingPipeHandler{release: release, called: called})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(DialOptions{Name: name, Timeout: time.Second, ExpectedServerPath: executable})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Execute(ctx, Request{Operation: OperationLoad, ModelID: "local-coding"})
		done <- err
	}()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("request not admitted")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled client still waiting for the handler")
	}
}

func TestPipeReadDeadlineDoesNotCancelAnAdmittedOperation(t *testing.T) {
	client, server := disposablePipePair(t, "read-budget")
	readCtx, cancelRead := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancelRead()
	server.readCtx = readCtx
	clientCtx, cancelClient := context.WithTimeout(context.Background(), time.Second)
	defer cancelClient()
	client.ctx = clientCtx
	served := make(chan error, 1)
	go func() { served <- ServeConn(context.Background(), server, pendingPipeHandler{release: readCtx.Done()}) }()
	result, err := exchange(client, Request{Operation: OperationLoad, ModelID: "local-coding"})
	if err != nil || result.Status != "completed" {
		t.Fatalf("read deadline canceled the admitted operation: result=%+v error=%v", result, err)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}

func disposablePipePair(t *testing.T, suffix string) (*Conn, *Conn) {
	t.Helper()
	name := testPipeName(t, suffix)
	listener, err := Listen(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan *Conn, 1)
	failed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			failed <- err
			return
		}
		accepted <- conn
	}()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client, err := Dial(context.Background(), DialOptions{Name: name, Timeout: time.Second, ExpectedServerPath: executable})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	select {
	case server := <-accepted:
		t.Cleanup(func() { _ = server.Close() })
		return client, server
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("pipe was not accepted")
	}
	return nil, nil
}

func TestPipeWriteDeadlineInterruptsAPeerThatDoesNotRead(t *testing.T) {
	client, _ := disposablePipePair(t, "write-deadline")
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	client.ctx = ctx
	started := time.Now()
	_, err := client.Write([]byte(strings.Repeat("x", pipeBufferBytes*4)))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("blocked write: elapsed=%v error=%v", time.Since(started), err)
	}
}

func TestPipeCloseInterruptsABlockedReadWithoutAClientDeadline(t *testing.T) {
	client, server := disposablePipePair(t, "close-read")
	readDone := make(chan error, 1)
	go func() { _, err := client.Read(make([]byte, 1)); readDone <- err }()
	select {
	case err := <-readDone:
		t.Fatalf("read did not wait: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	closed := make(chan struct{})
	go func() { _ = client.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		_ = server.Close()
		t.Fatal("Close failed to cancel read")
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("blocked read returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("read worker retained")
	}
}

func TestPipeFlushHasADeadlineEvenWithoutServerCancellation(t *testing.T) {
	client, server := disposablePipePair(t, "flush-deadline")
	if _, err := server.Write([]byte("response\n")); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { _ = server.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(responseDeadline + time.Second):
		_ = client.Close()
		t.Fatal("flush exceeded its delivery deadline")
	}
}
