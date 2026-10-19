package magiccache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sethvargo/go-githubactions"
)

// ToolsPath is where the agent registers a job's runtime token for the tool
// caches it serves, and the base of the path it answers with.
const ToolsPath = "/tools"

// ToolsURLEnv names the job variable that holds the registered base URL, for
// clients a step configures itself, such as <URL>/kv/<namespace>/ for a
// plain HTTP cache.
const ToolsURLEnv = "RUNS_ON_TOOLS_URL"

// ErrToolsNotServed is the registration's error on agents that do not serve
// the tool caches.
var ErrToolsNotServed = errors.New("this RunsOn agent does not serve the tool caches; upgrade it")

// Tools is a job's registration: the base URL of its tool caches, each
// protocol under its own path (URL + "/bazel", "/sccache" or
// "/kv/<namespace>"), and the ID in it, which stands for the job's runtime
// token and is therefore masked like a secret.
type Tools struct {
	URL string
	ID  string
}

// noRedirect keeps requests to the agent from following a redirect elsewhere.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func toolsURL() (*url.URL, error) {
	base, err := url.Parse(URL())
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		return nil, fmt.Errorf("invalid Magic Cache URL")
	}
	return base.ResolveReference(&url.URL{Path: ToolsPath}), nil
}

// Register exchanges the job's runtime token for the path of its tool caches.
// Only the agent keeps the token: the path is an ID the agent derives from it,
// which works only on this agent, for the token's scopes, until it expires.
// Registering the same token again returns the same path.
func Register(runtimeToken string) (Tools, error) {
	endpoint, err := toolsURL()
	if err != nil {
		return Tools{}, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return Tools{}, err
	}
	req.Header.Set("Authorization", "Bearer "+runtimeToken)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return Tools{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return Tools{}, ErrToolsNotServed
	}
	if resp.StatusCode != http.StatusOK {
		return Tools{}, fmt.Errorf("the agent answered the registration with HTTP %d", resp.StatusCode)
	}
	var registration struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&registration); err != nil {
		return Tools{}, fmt.Errorf("invalid registration response")
	}
	// The path holds a credential: a malformed one is refused without being
	// logged.
	id, ok := strings.CutPrefix(registration.Path, ToolsPath+"/")
	if !ok || len(id) < 32 || len(id) > 128 || strings.Trim(id, "0123456789abcdef") != "" {
		return Tools{}, fmt.Errorf("invalid registration path")
	}
	return Tools{URL: endpoint.String() + "/" + id, ID: id}, nil
}

// ServesTools reports whether the runner's agent serves the tool caches: it
// refuses a registration without a token with 401, where earlier agents do
// not know the path.
func ServesTools() bool {
	endpoint, err := toolsURL()
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: noRedirect}
	resp, err := client.Post(endpoint.String(), "", nil)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusUnauthorized
}

// registrations keeps, per agent and runtime token, the outcome of this process's
// registration: the tool inputs of one action step share it, so an agent
// that stops answering costs one timeout, not one per tool.
var registrations = struct {
	sync.Mutex
	byToken map[string]registration
}{byToken: map[string]registration{}}

type registration struct {
	tools Tools
	err   error
}

func registerOnce(runtimeToken string) (Tools, error) {
	key := URL() + "\x00" + runtimeToken
	registrations.Lock()
	defer registrations.Unlock()
	if done, ok := registrations.byToken[key]; ok {
		return done.tools, done.err
	}
	tools, err := Register(runtimeToken)
	registrations.byToken[key] = registration{tools, err}
	return tools, err
}

// RegisterFor registers the job for a tool cache, or warns why it cannot.
// input names the action input, and fallback says what the job does instead,
// for example "Bazel builds without the remote cache". The ID is masked in
// the job's logs: it stands for the job's runtime token. Later steps get the
// base URL as RUNS_ON_TOOLS_URL.
func RegisterFor(action *githubactions.Action, input, fallback string, magicCache bool, runtimeToken string) (Tools, bool) {
	if !magicCache {
		action.Warningf("%s needs Magic Cache (extras=s3-cache, or a Fleet runner with Magic Cache); %s.", input, fallback)
		return Tools{}, false
	}
	if runtimeToken == "" {
		action.Warningf("%s needs the job's ACTIONS_RUNTIME_TOKEN, which this step did not receive; %s.", input, fallback)
		return Tools{}, false
	}
	tools, err := registerOnce(runtimeToken)
	if errors.Is(err, ErrToolsNotServed) {
		action.Warningf("%s needs RunsOn v3.6.0 or later, whose agent serves the tool caches; %s.", input, fallback)
		return Tools{}, false
	}
	if err != nil {
		action.Warningf("%s could not register the job with the RunsOn agent (%v); %s.", input, err, fallback)
		return Tools{}, false
	}
	action.AddMask(tools.ID)
	action.SetEnv(ToolsURLEnv, tools.URL)
	return tools, true
}
