package sccache

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
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
	if set := competingBackends(); len(set) > 0 {
		action.Warningf("These settings make sccache use another storage than the agent's WebDAV cache: %s. Unset them for sccache: true to take effect.", strings.Join(set, ", "))
	}
	action.AddMask(credential)
	action.SetEnv("SCCACHE_GHA_ENABLED", "false")
	action.SetEnv("SCCACHE_WEBDAV_ENDPOINT", endpoint)
	action.SetEnv("SCCACHE_WEBDAV_TOKEN", credential)
	action.SetEnv("RUSTC_WRAPPER", "sccache")
	action.Infof("Configured sccache to use the agent's WebDAV cache. Install sccache before compiling; its first client starts the server.")
}

// competingBackends are the settings in the step's environment that select a
// storage sccache ranks above WebDAV: S3, Redis, Memcached, GCS, the GitHub
// Actions cache and Azure. sccache ignores an empty value.
func competingBackends() []string {
	var set []string
	for _, name := range []string{
		"SCCACHE_BUCKET",
		"SCCACHE_REDIS", "SCCACHE_REDIS_ENDPOINT", "SCCACHE_REDIS_CLUSTER_ENDPOINTS",
		"SCCACHE_MEMCACHED", "SCCACHE_MEMCACHED_ENDPOINT",
		"SCCACHE_GCS_BUCKET",
		"SCCACHE_GHA_VERSION",
		"SCCACHE_AZURE_BLOB_CONTAINER",
	} {
		if os.Getenv(name) != "" {
			set = append(set, name)
		}
	}
	// The action exports SCCACHE_GHA_ENABLED=false, which an env: of the job
	// or of a step can still override.
	switch strings.ToLower(os.Getenv("SCCACHE_GHA_ENABLED")) {
	case "true", "on", "1":
		set = append(set, "SCCACHE_GHA_ENABLED")
	}
	return set
}

// sccacheEndpoint is the agent's sccache WebDAV endpoint under the Magic
// Cache URL.
func sccacheEndpoint(cacheURL string) (string, error) {
	base, err := url.Parse(cacheURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		return "", fmt.Errorf("invalid Magic Cache URL")
	}
	return base.ResolveReference(&url.URL{Path: "/sccache"}).String(), nil
}

// noRedirect keeps requests to the agent from following a redirect elsewhere.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// servesWebDAV reports whether the runner's agent serves sccache's WebDAV
// endpoint: such an agent refuses a request without a credential with 401,
// while earlier agents do not know the path.
func servesWebDAV(cacheURL string) bool {
	endpoint, err := sccacheEndpoint(cacheURL)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: noRedirect}
	resp, err := client.Get(endpoint + "/")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusUnauthorized
}

func registerWebDAV(cacheURL, runtimeToken string) (string, string, error) {
	endpoint, err := sccacheEndpoint(cacheURL)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+runtimeToken)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirect}
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
	return endpoint, registration.Token, nil
}
