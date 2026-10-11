package gitproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Environment variables understood by the --git-proxy-serve entrypoint.
const (
	EnvMirrorDir   = "RUNS_ON_GIT_PROXY_MIRROR_DIR"
	EnvStateFile   = "RUNS_ON_GIT_PROXY_STATE_FILE"
	EnvOwner       = "RUNS_ON_GIT_PROXY_OWNER"
	EnvHealthToken = "RUNS_ON_GIT_PROXY_HEALTH_TOKEN"
	// EnvClientToken is the opaque per-job secret expected in client
	// Authorization headers; like the health token it travels through
	// GITHUB_ENV between steps.
	EnvClientToken = "RUNS_ON_GIT_PROXY_CLIENT_TOKEN"
	EnvRepository  = "RUNS_ON_GIT_PROXY_REPOSITORY"
	EnvRef         = "RUNS_ON_GIT_PROXY_REF"
	EnvAllRefs     = "RUNS_ON_GIT_PROXY_ALL_REFS"
	EnvDebug       = "RUNS_ON_GIT_PROXY_DEBUG"
)

// State is what the proxy publishes once it is listening; the action's setup
// step polls the state file, and the post step uses it to find pid and port.
type State struct {
	Port      int    `json:"port"`
	PID       int    `json:"pid"`
	MirrorDir string `json:"mirrorDir"`
	Owner     string `json:"owner"`
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

// readTokenFrom reads a single whitespace-trimmed credential from r,
// tolerating a closed or empty stream (no token configured).
func readTokenFrom(r io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// runFromEnv serves the proxy on an ephemeral localhost port until SIGTERM,
// publishing {port, pid} to the state file once ready.
func runFromEnv(ctx context.Context) error {
	mirrorDir := os.Getenv(EnvMirrorDir)
	stateFile := os.Getenv(EnvStateFile)
	owner := os.Getenv(EnvOwner)
	healthToken := os.Getenv(EnvHealthToken)
	clientToken := os.Getenv(EnvClientToken)
	if mirrorDir == "" || stateFile == "" || owner == "" || healthToken == "" || clientToken == "" {
		return fmt.Errorf("%s, %s, %s, %s, and %s must be set", EnvMirrorDir, EnvStateFile, EnvOwner, EnvHealthToken, EnvClientToken)
	}

	level := slog.LevelInfo
	if os.Getenv(EnvDebug) != "" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	server, err := NewServer(Options{
		MirrorDir:   mirrorDir,
		HealthToken: healthToken,
		ClientToken: clientToken,
		// The upstream credential arrives on stdin rather than the
		// environment: /proc/<pid>/environ stays readable by any same-uid
		// process for this process's whole lifetime, while stdin is consumed
		// once here and never observable again.
		UpstreamToken: readTokenFrom(os.Stdin),
		Repository:    os.Getenv(EnvRepository),
		Ref:           os.Getenv(EnvRef),
		AllRefs:       os.Getenv(EnvAllRefs) == "true",
		Logger:        log,
	})
	if err != nil {
		return err
	}
	defer server.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	if err := writeStateFile(stateFile, State{Port: port, PID: os.Getpid(), MirrorDir: mirrorDir, Owner: owner}); err != nil {
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
	dir := filepath.Dir(path)
	if err := ensureRealStateDirectory(dir); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func ensureRealStateDirectory(dir string) error {
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("state directory %s is not absolute", dir)
	}
	volumeRoot := filepath.VolumeName(dir) + string(filepath.Separator)
	rel, err := filepath.Rel(volumeRoot, dir)
	if err != nil {
		return fmt.Errorf("resolve state directory %s: %w", dir, err)
	}
	components := strings.Split(rel, string(filepath.Separator))
	if len(components) < 2 {
		return fmt.Errorf("state directory %s must be below a top-level directory", dir)
	}
	// Trust the root-owned top-level directory itself (/tmp, /var, C:\Users)
	// but create and validate every component below it without following links.
	current := filepath.Join(volumeRoot, components[0])
	for _, component := range components[1:] {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil && !os.IsExist(err) {
				return fmt.Errorf("create state directory %s: %w", current, err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return fmt.Errorf("inspect state directory %s: %w", current, err)
		}
		if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return fmt.Errorf("state path component %s is not a real directory", current)
		}
	}
	return nil
}
