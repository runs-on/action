package gocacheprog

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// diskStore is the local layer. The go command reads every hit from the
// returned DiskPath, so even remote hits land here first. Outputs live at
// o/<outputID> and action entries at a/<actionID>, whose content is
// "<outputID hex> <size> <unix nanos>".
type diskStore struct {
	dir string
}

func newDiskStore(dir string) (*diskStore, error) {
	for _, sub := range []string{"a", "o", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &diskStore{dir: dir}, nil
}

func (d *diskStore) outputPath(outputID []byte) string {
	return filepath.Join(d.dir, "o", hex.EncodeToString(outputID))
}

func (d *diskStore) get(actionID []byte) (Entry, bool) {
	data, err := os.ReadFile(filepath.Join(d.dir, "a", hex.EncodeToString(actionID)))
	if err != nil {
		return Entry{}, false
	}
	fields := strings.Fields(string(data))
	if len(fields) != 3 {
		return Entry{}, false
	}
	outputID, err := hex.DecodeString(fields[0])
	if err != nil {
		return Entry{}, false
	}
	size, err1 := strconv.ParseInt(fields[1], 10, 64)
	nanos, err2 := strconv.ParseInt(fields[2], 10, 64)
	if err1 != nil || err2 != nil {
		return Entry{}, false
	}
	path := d.outputPath(outputID)
	info, err := os.Stat(path)
	if err != nil || info.Size() != size {
		return Entry{}, false
	}
	return Entry{OutputID: outputID, Size: size, Time: time.Unix(0, nanos), DiskPath: path}, true
}

func (d *diskStore) put(actionID, outputID, body []byte, now time.Time) (string, error) {
	path := d.outputPath(outputID)
	if info, err := os.Stat(path); err != nil || info.Size() != int64(len(body)) {
		if err := d.writeAtomically(path, body); err != nil {
			return "", err
		}
	}
	entry := fmt.Sprintf("%x %d %d\n", outputID, len(body), now.UnixNano())
	if err := d.writeAtomically(filepath.Join(d.dir, "a", hex.EncodeToString(actionID)), []byte(entry)); err != nil {
		return "", err
	}
	return path, nil
}

func (d *diskStore) writeAtomically(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Join(d.dir, "tmp"), "put-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
