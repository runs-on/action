package sccache

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/runs-on/action/internal/magiccache"
	"github.com/sethvargo/go-githubactions"
)

// configureMagicCache registers the job with the agent and exports WebDAV
// settings. The first compiler invocation starts sccache in the job's own
// environment; the action does not require or manage a sccache process.
func configureMagicCache(action *githubactions.Action, magicCache bool, runtimeToken string) {
	if !magicCache {
		action.Warningf("sccache: true needs Magic Cache; sccache is not configured.")
		return
	}
	if runtimeToken == "" {
		action.Warningf("sccache: true needs the job's ACTIONS_RUNTIME_TOKEN; sccache is not configured.")
		return
	}
	endpoint, credential, err := registerWebDAV(magiccache.URL(), runtimeToken)
	if err != nil {
		action.Warningf("sccache: true could not register with the agent (%v); sccache is not configured.", err)
		return
	}
	action.AddMask(credential)
	action.SetEnv("SCCACHE_GHA_ENABLED", "false")
	action.SetEnv("SCCACHE_WEBDAV_ENDPOINT", endpoint)
	action.SetEnv("SCCACHE_WEBDAV_TOKEN", credential)
	action.SetEnv("RUSTC_WRAPPER", "sccache")
	action.Infof("Configured sccache to use the agent's WebDAV cache. Install sccache before compiling; its first client starts the server.")
}

func registerWebDAV(cacheURL, runtimeToken string) (string, string, error) {
	base, err := url.Parse(cacheURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		return "", "", fmt.Errorf("invalid Magic Cache URL")
	}
	req, err := http.NewRequest(http.MethodPost, base.ResolveReference(&url.URL{Path: "/sccache"}).String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+runtimeToken)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return "", "", fmt.Errorf("this RunsOn agent does not serve the sccache WebDAV endpoint; upgrade the agent")
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("agent returned HTTP %d", resp.StatusCode)
	}
	var registration struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&registration); err != nil {
		return "", "", fmt.Errorf("invalid agent registration response")
	}
	// The agent issues a 256-bit hex credential. Reject malformed replies
	// without logging their body, which could contain a credential.
	if len(registration.Token) != 64 {
		return "", "", fmt.Errorf("invalid agent cache credential")
	}
	for _, ch := range registration.Token {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return "", "", fmt.Errorf("invalid agent cache credential")
		}
	}
	return base.ResolveReference(&url.URL{Path: "/sccache"}).String(), registration.Token, nil
}
