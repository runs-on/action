package costs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runs-on/action/internal/config"
	"github.com/sethvargo/go-githubactions"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestComputeAndDisplayCostsResponses(t *testing.T) {
	for _, mode := range []string{"inline", "summary"} {
		for _, tc := range []struct {
			name         string
			status       int
			body         string
			transportErr error
			wantErr      string
		}{
			{name: "unavailable", status: 204},
			{name: "available", status: 200, body: `{"instanceType":"m7i.large","region":"eu-west-3","totalCost":0.12}`},
			{name: "not found", status: 404, body: `{"error":"not found"}`, wantErr: "404 Not Found"},
			{name: "server error", status: 500, body: `{"error":"failed"}`, wantErr: "500 Internal Server Error"},
			{name: "invalid response", status: 200, body: `{`, wantErr: "failed to decode"},
			{name: "network failure", transportErr: errors.New("connection failed"), wantErr: "failed to send"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				t.Setenv("RUNS_ON_INSTANCE_LAUNCHED_AT", "2026-01-01T00:00:00Z")
				t.Setenv("RUNS_ON_AWS_REGION", "eusc-de-east-1")
				t.Setenv("RUNS_ON_AWS_AZ", "") // Avoid unrelated EC2 zone discovery.
				t.Setenv("RUNS_ON_INSTANCE_TYPE", "m7i.large")
				summary := filepath.Join(t.TempDir(), "summary")
				t.Setenv("GITHUB_STEP_SUMMARY", summary)
				oldTransport := http.DefaultTransport
				http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != costAPIURL || r.Method != http.MethodPost {
						t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
					}
					if tc.transportErr != nil {
						return nil, tc.transportErr
					}
					return &http.Response{StatusCode: tc.status, Status: fmt.Sprintf("%d %s", tc.status, http.StatusText(tc.status)), Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
				})
				t.Cleanup(func() { http.DefaultTransport = oldTransport })
				output, err := os.CreateTemp(t.TempDir(), "stdout")
				if err != nil {
					t.Fatal(err)
				}
				defer output.Close()
				oldStdout := os.Stdout
				os.Stdout = output
				t.Cleanup(func() { os.Stdout = oldStdout })
				var logs bytes.Buffer
				err = ComputeAndDisplayCosts(githubactions.New(githubactions.WithWriter(&logs)), &config.Config{ShowCosts: mode})
				os.Stdout = oldStdout
				if tc.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
						t.Fatalf("error=%v, want %q", err, tc.wantErr)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				printed, _ := os.ReadFile(output.Name())
				written, _ := os.ReadFile(summary)
				if tc.status == 204 {
					want := "Skipping cost report: pricing data is unavailable for instance m7i.large in region eusc-de-east-1."
					if !strings.Contains(logs.String(), want) || strings.Contains(logs.String(), "::warning") {
						t.Fatalf("logs=%q", logs.String())
					}
				}
				if tc.status == 200 && tc.wantErr == "" {
					if !strings.Contains(string(printed), "$0.1200") {
						t.Fatalf("missing costs: %s", printed)
					}
					if mode == "summary" && !bytes.Equal(bytes.TrimSpace(printed), bytes.TrimSpace(written)) {
						t.Fatalf("summary differs from output: %q", written)
					}
				} else if len(printed) != 0 || len(written) != 0 {
					t.Fatalf("unexpected cost output: %q, summary: %q", printed, written)
				}
			})
		}
	}
}
