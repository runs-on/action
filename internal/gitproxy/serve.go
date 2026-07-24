package gitproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// Environment variables understood by the --git-proxy-serve entrypoint.
const (
	EnvMirrorDir = "RUNS_ON_GIT_PROXY_MIRROR_DIR"
	EnvStateFile = "RUNS_ON_GIT_PROXY_STATE_FILE"
	EnvDebug     = "RUNS_ON_GIT_PROXY_DEBUG"
)

// State is what the proxy publishes once it is listening; the action's setup
// step polls the state file, and the post step uses it to find pid and port.
type State struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

// ReadStateFile loads a previously published proxy state.
func ReadStateFile(path string) (State, error) {
	var st State
	data, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("parse %s: %w", path, err)
	}
	if st.Port <= 0 {
		return st, fmt.Errorf("invalid port %d in %s", st.Port, path)
	}
	return st, nil
}

// runFromEnv serves the proxy on an ephemeral localhost port until SIGTERM,
// publishing {port, pid} to the state file once ready.
func runFromEnv(ctx context.Context) error {
	mirrorDir := os.Getenv(EnvMirrorDir)
	stateFile := os.Getenv(EnvStateFile)
	if mirrorDir == "" || stateFile == "" {
		return fmt.Errorf("%s and %s must be set", EnvMirrorDir, EnvStateFile)
	}

	level := slog.LevelInfo
	if os.Getenv(EnvDebug) != "" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	server, err := NewServer(Options{MirrorDir: mirrorDir, Logger: log})
	if err != nil {
		return err
	}
	defer server.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	if err := writeStateFile(stateFile, State{Port: port, PID: os.Getpid()}); err != nil {
		ln.Close()
		return err
	}
	defer os.Remove(stateFile)
	log.Info("git proxy listening", "port", port, "mirrorDir", mirrorDir)

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	srv := &http.Server{Handler: server.Handler()}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("git proxy shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// writeStateFile publishes the state atomically (temp file + rename) so the
// polling parent never reads a partial file.
func writeStateFile(path string, st State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
