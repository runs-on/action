package gitproxy

import (
	"io"
	"net/http"
)

// hopHeaders are connection-level headers that must not be forwarded.
var hopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// forwardUpstream transparently replays the request against the real host
// (https://{host}{rest}), streaming the response back. Used for everything
// the mirror cannot or should not serve: LFS endpoints, receive-pack,
// requests for objects missing from the mirror, or mirror failures. The
// client's own Authorization header travels with the request, so upstream
// enforces the exact same permissions as a direct call.
func (s *Server) forwardUpstream(w http.ResponseWriter, r *http.Request, t target, body io.Reader, reason string) {
	url := s.upstreamBase(t.host) + t.rest
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	for _, h := range hopHeaders {
		req.Header.Del(h)
	}
	req.Host = t.host

	// RoundTrip (not http.Client) so redirects pass through to the git/LFS
	// client untouched, exactly as if it had talked to upstream directly.
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		s.log.Error("upstream forward failed", "url", url, "reason", reason, "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	s.log.Info("forwarded upstream", "url", url, "reason", reason, "status", resp.StatusCode)
	header := w.Header()
	for k, vv := range resp.Header {
		for _, v := range vv {
			header.Add(k, v)
		}
	}
	for _, h := range hopHeaders {
		header.Del(h)
	}
	header.Set(statusHeader, "upstream-"+reason)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(&flushWriter{w}, resp.Body); err != nil {
		s.log.Warn("upstream response copy interrupted", "url", url, "err", err)
	}
}

// flushWriter flushes after each write so pack data streams to the git
// client instead of buffering the whole response.
type flushWriter struct {
	w http.ResponseWriter
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if f, ok := fw.w.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}
