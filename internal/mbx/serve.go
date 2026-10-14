package mbx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/runs-on/action/internal/magiccache"
)

// Flag runs this binary as the helper (internal, started by the action).
const Flag = "--mbx-serve"

// The helper's settings, from the action that starts it. The job's runtime
// token and the client token come on stdin instead: /proc/<pid>/environ stays
// readable by any process of the runner's user for the helper's whole
// lifetime, while stdin is read once.
const (
	envStateFile  = "RUNS_ON_MBX_STATE_FILE"
	envResultsURL = "RUNS_ON_MBX_RESULTS_URL"
)

// State is what the helper publishes once it listens.
type State struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

// Serve runs the helper until it is stopped: it listens on an ephemeral
// loopback port and publishes the port and its PID in the state file.
func Serve(ctx context.Context, stdin io.Reader) error {
	stateFile, resultsURL := os.Getenv(envStateFile), os.Getenv(envResultsURL)
	if stateFile == "" || resultsURL == "" {
		return fmt.Errorf("%s and %s must be set", envStateFile, envResultsURL)
	}
	runtimeToken, clientToken, err := readTokens(stdin)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	server := NewServer(magiccache.NewCacheClient(resultsURL, runtimeToken), clientToken, log)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if err := writeState(stateFile, State{Port: ln.Addr().(*net.TCPAddr).Port, PID: os.Getpid()}); err != nil {
		_ = ln.Close()
		return err
	}
	log.Info("mbx helper listening", "addr", ln.Addr().String(), "cache", resultsURL)

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()
	srv := &http.Server{Handler: server, ReadHeaderTimeout: 30 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	stats, _ := json.Marshal(server.Stats())
	log.Info("mbx helper stopping", "stats", string(stats))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func readTokens(r io.Reader) (runtimeToken, clientToken string, err error) {
	scanner := bufio.NewScanner(io.LimitReader(r, 64<<10))
	scanner.Buffer(make([]byte, 0, 16<<10), 64<<10)
	var lines []string
	for scanner.Scan() && len(lines) < 2 {
		lines = append(lines, strings.TrimSpace(scanner.Text()))
	}
	if len(lines) < 2 || lines[0] == "" || lines[1] == "" {
		return "", "", errors.New("the runtime token and the client token must come on stdin")
	}
	return lines[0], lines[1], nil
}

// writeState publishes the state atomically, so the action never reads half
// of it.
func writeState(path string, state State) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readState(path string) (State, error) {
	var state State
	data, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Port <= 0 || state.PID <= 0 {
		return state, fmt.Errorf("invalid helper state in %s", path)
	}
	return state, nil
}
