package gitproxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"regexp"
	"strconv"
)

// maxParseBody bounds how much of an upload-pack request we buffer for want
// inspection. Wants precede the potentially large list of haves, so the
// buffered prefix is still inspected when the request exceeds this limit.
const maxParseBody = 4 << 20

var wantRe = regexp.MustCompile(`(?m)^want ([0-9a-f]{40,64})`)

// handleUploadPack serves a POST git-upload-pack request from the local
// mirror, unless a wanted object is absent (e.g. a SHA orphaned by a
// force-push between the workflow event and now): those requests are
// forwarded upstream so the fetch behaves exactly like a vanilla checkout.
func (s *Server) handleUploadPack(w http.ResponseWriter, r *http.Request, t target) {
	repoPath := s.mirror.RepoPath(t.host, t.owner, t.repo)

	body, overflow, err := bufferBody(r.Body, maxParseBody)
	if err != nil {
		s.log.Error("read upload-pack body failed", "repo", t.repoKey(), "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Restore the body for whichever handler serves the request.
	restored := io.Reader(bytes.NewReader(body))
	if overflow != nil {
		restored = io.MultiReader(restored, overflow)
	}

	if s.mirror.SyncFailed(t.host, t.owner, t.repo) {
		s.log.Warn("mirror sync previously failed, forwarding upload-pack upstream", "repo", t.repoKey())
		s.forwardUpstream(w, r, t, restored, "mirror-sync-failed")
		return
	}

	if err := s.mirror.PrepareForServe(r.Context(), t.host, t.owner, t.repo); err != nil {
		s.log.Warn("mirror unavailable for upload-pack, forwarding upstream", "repo", t.repoKey())
		s.forwardUpstream(w, r, t, restored, "mirror-unavailable")
		return
	}

	upstreamURL := s.upstreamBase(t.host) + "/" + t.owner + "/" + t.repo + ".git"
	if err := s.mirror.authorizeRepo(r.Context(), t.repoKey(), repoPath, upstreamURL, s.upstreamAuth(r)); err != nil {
		s.log.Warn("private mirror authorization failed, forwarding upload-pack upstream", "repo", t.repoKey(), "err", err)
		s.forwardUpstream(w, r, t, restored, "authentication-required")
		return
	}

	for _, want := range parseWants(body, r.Header.Get("Content-Encoding")) {
		if !s.mirror.HasObject(r.Context(), repoPath, want) {
			s.log.Warn("want not in mirror, forwarding upstream", "repo", t.repoKey(), "want", want)
			s.forwardUpstream(w, r, t, restored, "missing-want")
			return
		}
	}

	req := r.Clone(r.Context())
	req.Body = io.NopCloser(restored)
	s.serveCGI(w, req, t.cgiPath("/git-upload-pack"))
}

// bufferBody reads up to limit bytes. When the body is larger, the buffered
// prefix is returned along with the unread remainder as overflow.
func bufferBody(body io.Reader, limit int) ([]byte, io.Reader, error) {
	buf := bytes.NewBuffer(nil)
	n, err := io.CopyN(buf, body, int64(limit)+1)
	if err != nil && err != io.EOF {
		return nil, nil, err
	}
	if n > int64(limit) {
		return buf.Bytes(), body, nil
	}
	return buf.Bytes(), nil, nil
}

// parseWants extracts wanted object IDs from an upload-pack request body,
// handling gzip bodies and both protocol v0/v1 and v2 framing. Best-effort:
// an empty result means "serve locally" (e.g. protocol v2 ls-refs commands
// have no wants).
func parseWants(body []byte, contentEncoding string) []string {
	if contentEncoding == "gzip" {
		gz, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil
		}
		decoded, err := io.ReadAll(io.LimitReader(gz, maxParseBody))
		// A large compressed request leaves the buffered gzip stream
		// incomplete. Its decoded prefix still contains the want lines and is
		// safe to inspect even when the reader reports an unexpected EOF.
		if err != nil && len(decoded) == 0 {
			return nil
		}
		body = decoded
	}
	var wants []string
	for _, match := range wantRe.FindAllSubmatch(pktPayload(body), -1) {
		wants = append(wants, string(match[1]))
	}
	return wants
}

// pktPayload strips pkt-line framing, concatenating payloads separated by
// newlines. Flush (0000), delim (0001) and response-end (0002) packets are
// skipped — protocol v2 places wants after a delim packet, so treating
// special packets as terminators would hide them. Tolerant to malformed
// input: returns the raw bytes when nothing parses.
func pktPayload(b []byte) []byte {
	var out []byte
	i := 0
	for i+4 <= len(b) {
		n64, err := strconv.ParseInt(string(b[i:i+4]), 16, 32)
		if err != nil {
			break
		}
		n := int(n64)
		i += 4
		if n < 4 {
			continue
		}
		if i+n-4 > len(b) {
			break
		}
		out = append(out, b[i:i+n-4]...)
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		i += n - 4
	}
	if len(out) == 0 {
		return b
	}
	return out
}
