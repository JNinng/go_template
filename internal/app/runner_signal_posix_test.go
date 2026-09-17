//go:build !windows

package app

import (
	"context"
	"sync"
	"syscall"
	"testing"
	"time"
)

// 信号触发路径仅 POSIX 测试（Windows 无法向自身可靠发送 SIGTERM）。
func TestRun_SignalTriggersGracefulShutdown(t *testing.T) {
	r := new(runner)
	var mu sync.Mutex
	var events []string
	rec := func(e string) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, e)
			return nil
		}
	}
	r.Add("a", rec("start:a"), rec("stop:a"))
	r.Add("b", rec("start:b"), rec("stop:b"))

	go func() {
		time.Sleep(200 * time.Millisecond)
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
			t.Errorf("kill: %v", err)
		}
	}()

	done := make(chan error, 1)
	go func() { done <- r.Run() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown must return nil, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after signal")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"start:a", "start:b", "stop:b", "stop:a"}
	if len(events) != len(want) {
		t.Fatalf("events %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events %v, want %v", events, want)
		}
	}
}
