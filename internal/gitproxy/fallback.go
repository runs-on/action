package gitproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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

// isReceivePack identifies push traffic: the receive-pack POST itself and its
// info/refs handshake.
func isReceivePack(t target, r *http.Request) bool {
	return strings.HasSuffix(t.rest, "/git-receive-pack") ||
		r.URL.Query().Get("service") == "git-receive-pack"
}

// lfsBatchIsDownload reports whether an LFS batch request body asks for the
// read-only download operation. Anything unparseable fails closed.
func lfsBatchIsDownload(body []byte) bool {
	var batch struct {
		Operation string `json:"operation"`
	}
	return json.Unmarshal(body, &batch) == nil && batch.Operation == "download"
}

// forwardUpstream transparently replays the request against the real host
// (https://{host}{rest}), streaming the response back. Used for everything
// the mirror cannot or should not serve: LFS endpoints, receive-pack,
// requests for objects missing from the mirror, or mirror failures. Read
// operations map the opaque client credential to the real GitHub token via
// upstreamAuth; writes keep the client's own credential, so upstream enforces
// the exact same permissions as a direct call.
func (s *Server) forwardUpstream(w http.ResponseWriter, r *http.Request, t target, body io.Reader, reason string) {
	url := s.upstreamBase(t.host) + t.rest
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}

	// The opaque per-job client credential must never reach upstream; swap it
	// for the real token (or drop it) exactly like the mirror sync path.
	// Substitution is limited to read operations: legitimate pushes are pinned
	// upstream by pushInsteadOf with the pusher's own credential, and LFS
	// writes (uploads, locks) only occur in push flows, so granting the
	// proxy's token to any forwarded write would only serve a same-job client
	// trying to write to repositories it cannot itself access.
	substitute := !isReceivePack(t, r)
	if substitute && strings.Contains(t.rest, "/info/lfs/") {
		substitute = false
		if strings.HasSuffix(t.rest, "/info/lfs/objects/batch") && r.Header.Get("Content-Encoding") == "" {
			peek, err := io.ReadAll(io.LimitReader(body, 1<<20))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			substitute = lfsBatchIsDownload(peek)
			body = io.MultiReader(bytes.NewReader(peek), body)
		}
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
	req.Header.Del("Authorization")
	auth := r.Header.Get("Authorization")
	if substitute {
		auth = s.upstreamAuth(r)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
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
