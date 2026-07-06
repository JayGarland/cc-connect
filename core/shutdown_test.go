package core

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestShutdownServerHandleShutdown(t *testing.T) {
	var calls atomic.Int32
	s := NewShutdownServer("127.0.0.1:0", func() {
		calls.Add(1)
	})

	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req.RemoteAddr = "127.0.0.1:51234"
	rec := httptest.NewRecorder()

	s.handleShutdown(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("trigger calls = %d, want 1", got)
	}

	rec = httptest.NewRecorder()
	s.handleShutdown(rec, req)
	if got := calls.Load(); got != 1 {
		t.Fatalf("trigger calls after duplicate = %d, want 1", got)
	}
}

func TestShutdownServerRejectsNonPostAndNonLoopback(t *testing.T) {
	var calls atomic.Int32
	s := NewShutdownServer("127.0.0.1:0", func() {
		calls.Add(1)
	})

	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	req.RemoteAddr = "127.0.0.1:51234"
	rec := httptest.NewRecorder()
	s.handleShutdown(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}

	req = httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req.RemoteAddr = "203.0.113.10:51234"
	rec = httptest.NewRecorder()
	s.handleShutdown(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("trigger calls = %d, want 0", got)
	}
}

func TestShutdownServerStartServesLocalhostPost(t *testing.T) {
	done := make(chan struct{}, 1)
	s := NewShutdownServer("127.0.0.1:0", func() {
		done <- struct{}{}
	})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Stop(ctx); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}()

	resp, err := http.Post("http://"+s.Addr()+"/shutdown", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /shutdown: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, string(body))
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for shutdown trigger")
	}
}
