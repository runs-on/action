package gocacheprog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// storeClient reads and writes entries in the agent's gocacheprog cache: GET
// and PUT on <endpoint>/<key>. The endpoint's path is the job's credential.
// The agent reads the job's own branch scope first, then the scopes it may
// read (the base or default branch), and writes to the job's own scope.
type storeClient struct {
	endpoint string
	http     *http.Client
}

// callTimeout bounds the wait for the agent's answer: a cache that accepts
// connections but stops answering must not hold up the go command for the
// HTTP client's five-minute limit, which is sized for large transfers.
const callTimeout = 15 * time.Second

func newStoreClient(endpoint string) *storeClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 128
	transport.ResponseHeaderTimeout = callTimeout
	return &storeClient{
		endpoint: strings.TrimSuffix(endpoint, "/") + "/",
		http:     &http.Client{Transport: transport, Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

// open returns the content of the entry key, or ok=false on a miss. The
// caller closes it.
func (c *storeClient) open(ctx context.Context, key string) (io.ReadCloser, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+key, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp.Body, true, nil
	}
	// Reading the short error body lets the connection serve the next
	// request: a cold build misses thousands of times.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("read %s: HTTP %d", key, resp.StatusCode)
}

// put stores size bytes read from body as the entry key, replacing any.
func (c *storeClient) put(ctx context.Context, key string, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.endpoint+key, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("write %s: HTTP %d", key, resp.StatusCode)
	}
	return nil
}
