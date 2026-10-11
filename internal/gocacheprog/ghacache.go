package gocacheprog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ghaClient talks to the GitHub Actions cache service (v2, Twirp), as served
// by GitHub or by RunsOn's Magic Cache at ACTIONS_RESULTS_URL. Entries are
// immutable: a key that already exists cannot be created again.
type ghaClient struct {
	baseURL string
	token   string
	http    *http.Client
}

var errEntryExists = errors.New("cache entry already exists")

// errAnswered marks a request the cache service answered with an error: the
// service is reachable, and only that entry is lost. Magic Cache answers this
// way when another job replaces an entry while it is being downloaded.
var errAnswered = errors.New("the cache service refused the request")

const twirpPrefix = "twirp/github.actions.results.api.v1.CacheService/"

func newGHAClient(resultsURL, token string) *ghaClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 128
	return &ghaClient{
		baseURL: strings.TrimSuffix(resultsURL, "/") + "/",
		token:   token,
		http:    &http.Client{Transport: transport, Timeout: 5 * time.Minute},
	}
}

// twirpError is a Twirp error response.
type twirpError struct {
	Status int
	Code   string `json:"code"`
	Msg    string `json:"msg"`
}

func (e *twirpError) Error() string {
	return fmt.Sprintf("twirp %d %s: %s", e.Status, e.Code, e.Msg)
}

// callTimeout bounds one Twirp call: a cache that accepts connections but
// stops answering must not hold up the go command for the HTTP client's
// five-minute limit, which is sized for large blob transfers.
const callTimeout = 15 * time.Second

func (c *ghaClient) call(ctx context.Context, method string, req, resp any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+twirpPrefix+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = httpResp.Body.Close() }()
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return err
	}
	if httpResp.StatusCode != http.StatusOK {
		twErr := &twirpError{Status: httpResp.StatusCode}
		_ = json.Unmarshal(data, twErr)
		if twErr.Msg == "" {
			twErr.Msg = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("%w: %w", errAnswered, twErr)
	}
	return json.Unmarshal(data, resp)
}

// open returns the content of the entry key, or of the newest entry matching
// one of restoreKeys as a prefix, or ok=false on a miss. The caller closes it.
func (c *ghaClient) open(ctx context.Context, version, key string, restoreKeys ...string) (io.ReadCloser, bool, error) {
	var resp struct {
		Ok             bool   `json:"ok"`
		URL            string `json:"signedDownloadUrl"`
		URLSnakeCase   string `json:"signed_download_url"`
		MatchedKey     string `json:"matchedKey"`
		MatchedKeySnke string `json:"matched_key"`
	}
	req := map[string]any{"key": key, "version": version}
	if len(restoreKeys) > 0 {
		req["restore_keys"] = restoreKeys
	}
	if err := c.call(ctx, "GetCacheEntryDownloadURL", req, &resp); err != nil {
		return nil, false, err
	}
	url := firstNonEmpty(resp.URL, resp.URLSnakeCase)
	matched := firstNonEmpty(resp.MatchedKey, resp.MatchedKeySnke)
	if !resp.Ok || url == "" || (len(restoreKeys) == 0 && matched != "" && matched != key) {
		return nil, false, nil
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, false, err
	}
	if httpResp.StatusCode != http.StatusOK {
		_ = httpResp.Body.Close()
		if httpResp.StatusCode == http.StatusNotFound {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%w: download %s: HTTP %d", errAnswered, key, httpResp.StatusCode)
	}
	return httpResp.Body, true, nil
}

// upload creates the entry with a single blob PUT of size bytes read from
// body.
func (c *ghaClient) upload(ctx context.Context, version, key string, body io.Reader, size int64) error {
	var created struct {
		Ok           bool   `json:"ok"`
		URL          string `json:"signedUploadUrl"`
		URLSnakeCase string `json:"signed_upload_url"`
	}
	if err := c.call(ctx, "CreateCacheEntry", map[string]any{"key": key, "version": version}, &created); err != nil {
		var twErr *twirpError
		if errors.As(err, &twErr) && (twErr.Status == http.StatusConflict || twErr.Code == "already_exists") {
			return errEntryExists
		}
		return err
	}
	url := firstNonEmpty(created.URL, created.URLSnakeCase)
	if !created.Ok || url == "" {
		return errEntryExists
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return err
	}
	httpReq.ContentLength = size
	httpReq.Header.Set("x-ms-blob-type", "BlockBlob")
	httpReq.Header.Set("Content-Type", "application/octet-stream")
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, httpResp.Body)
	_ = httpResp.Body.Close()
	if httpResp.StatusCode/100 != 2 {
		return fmt.Errorf("%w: upload %s: HTTP %d", errAnswered, key, httpResp.StatusCode)
	}
	var finalized struct {
		Ok bool `json:"ok"`
	}
	req := map[string]any{"key": key, "version": version, "size_bytes": strconv.FormatInt(size, 10)}
	if err := c.call(ctx, "FinalizeCacheEntryUpload", req, &finalized); err != nil {
		return err
	}
	if !finalized.Ok {
		return fmt.Errorf("%w: finalize %s: not ok", errAnswered, key)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
