package magiccache

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/sethvargo/go-githubactions"
)

func TestUpdateZctionsConfig(t *testing.T) {
	const (
		resultsURL = "https://results.example/"
		cacheURL   = "https://ghes.example/_services/artifactcache/"
		token      = "test-runtime-token"
	)

	for _, tc := range []struct {
		name       string
		resultsURL string
		cacheURL   string
		want       url.Values
	}{
		{
			name:     "v1 cache URL only",
			cacheURL: cacheURL,
			want: url.Values{
				"ACTIONS_CACHE_URL":     {cacheURL},
				"ACTIONS_RUNTIME_TOKEN": {token},
			},
		},
		{
			name:       "results URL only",
			resultsURL: resultsURL,
			want: url.Values{
				"ACTIONS_RESULTS_URL":   {resultsURL},
				"ACTIONS_RUNTIME_TOKEN": {token},
			},
		},
		{
			name:       "both URLs",
			resultsURL: resultsURL,
			cacheURL:   cacheURL,
			want: url.Values{
				"ACTIONS_RESULTS_URL":   {resultsURL},
				"ACTIONS_CACHE_URL":     {cacheURL},
				"ACTIONS_RUNTIME_TOKEN": {token},
			},
		},
		{name: "neither URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan url.Values, 1)
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.URL.Path != "/config" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse config form: %v", err)
				}
				requests <- r.PostForm
				w.WriteHeader(http.StatusNoContent)
			}))
			defer agent.Close()

			var log bytes.Buffer
			action := githubactions.New(githubactions.WithWriter(&log))
			UpdateZctionsConfig(action, agent.URL+"/", tc.resultsURL, tc.cacheURL, token)

			select {
			case got := <-requests:
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("config form = %v, want %v", got, tc.want)
				}
			default:
				if tc.want != nil {
					t.Fatal("no config request sent")
				}
			}
			if strings.Contains(log.String(), token) {
				t.Error("runtime token was logged")
			}
		})
	}
}
