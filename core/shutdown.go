package core

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
)

const DefaultShutdownAddr = "127.0.0.1:29344"

// ShutdownServer exposes a localhost-only endpoint that asks the main process
// to run its normal graceful shutdown path.
type ShutdownServer struct {
	addr    string
	server  *http.Server
	trigger func()
	once    sync.Once
}

func NewShutdownServer(addr string, trigger func()) *ShutdownServer {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = DefaultShutdownAddr
	}
	return &ShutdownServer{addr: addr, trigger: trigger}
}

func (s *ShutdownServer) Addr() string {
	if s == nil {
		return ""
	}
	return s.addr
}

func (s *ShutdownServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/shutdown", s.handleShutdown)

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.addr = ln.Addr().String()
	s.server = &http.Server{Handler: mux}

	go func() {
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("shutdown server error", "error", err)
		}
	}()
	slog.Info("shutdown server started", "addr", s.addr)
	return nil
}

func (s *ShutdownServer) Stop(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *ShutdownServer) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRemoteAddr(r.RemoteAddr) {
		http.Error(w, "localhost only", http.StatusForbidden)
		return
	}

	s.once.Do(func() {
		if s.trigger != nil {
			go s.trigger()
		}
	})
	apiJSON(w, http.StatusAccepted, map[string]string{"status": "shutting_down"})
}

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return strings.EqualFold(host, "localhost")
	}
	return ip.IsLoopback()
}
