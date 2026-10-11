// The GOCACHEPROG helper: this action's binary, run with --gocacheprog, serves
// the go command's build cache (https://pkg.go.dev/cmd/go/internal/cacheprog,
// Go 1.24+). It keeps the cache on local disk and shares it between jobs
// through the GitHub Actions cache service, which Magic Cache serves from S3
// with GitHub's branch scoping. Each go command prefetches the entries the
// previous run of the same command used. It lives in the action, not in the
// RunsOn agent, because the action's directory is also mounted in job
// containers.
package gocacheprog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Cmd is a GOCACHEPROG command.
type Cmd string

const (
	CmdGet   Cmd = "get"
	CmdPut   Cmd = "put"
	CmdClose Cmd = "close"
)

// Request is a request from the go command. The body of a put follows its
// request on the wire as a JSON-encoded []byte.
type Request struct {
	ID       int64
	Command  Cmd
	ActionID []byte `json:",omitempty"`
	OutputID []byte `json:",omitempty"`
	BodySize int64  `json:",omitempty"`
	// ObjectID is OutputID's name before Go 1.24.
	ObjectID []byte `json:",omitempty"`
}

// Response answers a Request. The first response, with ID 0, is sent unprompted
// and lists the supported commands.
type Response struct {
	ID            int64
	Err           string     `json:",omitempty"`
	KnownCommands []Cmd      `json:",omitempty"`
	Miss          bool       `json:",omitempty"`
	OutputID      []byte     `json:",omitempty"`
	Size          int64      `json:",omitempty"`
	Time          *time.Time `json:",omitempty"`
	DiskPath      string     `json:",omitempty"`
}

// Handler serves the cache commands.
type Handler interface {
	Get(actionID []byte) (Entry, bool, error)
	Put(actionID, outputID []byte, body []byte) (diskPath string, err error)
	Close() error
}

// Entry is a cache hit.
type Entry struct {
	OutputID []byte
	Size     int64
	Time     time.Time
	DiskPath string
}

// Serve runs the protocol until the go command sends close or closes stdin.
// Requests are handled concurrently, as the go command issues them from its
// parallel build actions.
func Serve(in io.Reader, out io.Writer, h Handler) error {
	bw := bufio.NewWriter(out)
	enc := json.NewEncoder(bw)
	var mu sync.Mutex
	write := func(resp *Response) error {
		mu.Lock()
		defer mu.Unlock()
		if err := enc.Encode(resp); err != nil {
			return err
		}
		return bw.Flush()
	}
	if err := write(&Response{KnownCommands: []Cmd{CmdGet, CmdPut, CmdClose}}); err != nil {
		return err
	}

	dec := json.NewDecoder(bufio.NewReader(in))
	var wg sync.WaitGroup
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			wg.Wait()
			if err == io.EOF {
				return h.Close()
			}
			return err
		}
		var body []byte
		if req.Command == CmdPut && req.BodySize > 0 {
			if err := dec.Decode(&body); err != nil {
				return fmt.Errorf("read body of put %d: %w", req.ID, err)
			}
			if int64(len(body)) != req.BodySize {
				return fmt.Errorf("put %d: body is %d bytes, want %d", req.ID, len(body), req.BodySize)
			}
		}
		if req.Command == CmdClose {
			wg.Wait()
			resp := &Response{ID: req.ID}
			if err := h.Close(); err != nil {
				resp.Err = err.Error()
			}
			return write(resp)
		}
		wg.Go(func() {
			_ = write(handle(h, &req, body))
		})
	}
}

func handle(h Handler, req *Request, body []byte) *Response {
	resp := &Response{ID: req.ID}
	switch req.Command {
	case CmdGet:
		entry, ok, err := h.Get(req.ActionID)
		switch {
		case err != nil:
			resp.Err = err.Error()
		case !ok:
			resp.Miss = true
		default:
			resp.OutputID = entry.OutputID
			resp.Size = entry.Size
			resp.DiskPath = entry.DiskPath
			if !entry.Time.IsZero() {
				t := entry.Time
				resp.Time = &t
			}
		}
	case CmdPut:
		outputID := req.OutputID
		if len(outputID) == 0 {
			outputID = req.ObjectID
		}
		path, err := h.Put(req.ActionID, outputID, body)
		if err != nil {
			resp.Err = err.Error()
		} else {
			resp.DiskPath = path
		}
	default:
		resp.Err = fmt.Sprintf("unknown command %q", req.Command)
	}
	return resp
}
