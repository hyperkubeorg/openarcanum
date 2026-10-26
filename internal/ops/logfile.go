package ops

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// RotatingFile is an append-only log file that rotates itself: before a
// write that would take it past maxSize bytes, or once it has been open for
// maxAge, it is renamed to "<path>.<UTC time>" and a new file is started;
// only the newest keep rotated files are kept.
//
// Rotation is in-process rather than left to logrotate so a node needs no
// cooperation from the host (no copytruncate races, no SIGHUP handling) on
// installs without journald, such as a Raspberry Pi running the binary from
// a shell or a minimal container with a file volume. Under systemd or a
// container runtime the default is stderr, which those rotate themselves.
type RotatingFile struct {
	path    string
	maxSize int64
	maxAge  time.Duration
	keep    int
	now     func() time.Time
	errOut  io.Writer

	mu       sync.Mutex
	f        *os.File
	size     int64
	opened   time.Time
	reported bool
}

// rotatedSuffix matches the suffix rotate adds.
var rotatedSuffix = regexp.MustCompile(`^\.\d{8}T\d{6}\.\d{3}Z(-\d+)?$`)

// OpenRotating opens (creating) path for appending. A non-empty existing
// file older than maxAge (by modification time) is rotated first. errOut
// receives a one-line notice if rotating ever fails (logging then carries
// on in the current file).
func OpenRotating(path string, maxSize int64, maxAge time.Duration, keep int, errOut io.Writer) (*RotatingFile, error) {
	if errOut == nil {
		errOut = io.Discard
	}
	r := &RotatingFile{path: path, maxSize: maxSize, maxAge: maxAge, keep: keep, now: time.Now, errOut: errOut}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := r.open(); err != nil {
		return nil, err
	}
	if st, err := r.f.Stat(); err == nil && st.Size() > 0 && maxAge > 0 && r.now().Sub(st.ModTime()) >= maxAge {
		if err := r.rotate(); err != nil {
			r.report(err)
		}
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size, r.opened = f, st.Size(), r.now()
	return nil
}

// Write appends p, rotating first if needed. One call is one log record:
// a record is never split across files.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size > 0 && (r.size+int64(len(p)) > r.maxSize || (r.maxAge > 0 && r.now().Sub(r.opened) >= r.maxAge)) {
		if err := r.rotate(); err != nil {
			r.report(err)
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate renames the current file aside, starts a new one and prunes old
// ones. On a failed rename it keeps writing to the current file.
func (r *RotatingFile) rotate() error {
	name := r.path + "." + r.now().UTC().Format("20060102T150405.000Z")
	for i := 1; ; i++ {
		if _, err := os.Lstat(name); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s.%s-%d", r.path, r.now().UTC().Format("20060102T150405.000Z"), i)
	}
	if err := os.Rename(r.path, name); err != nil {
		return err
	}
	old := r.f
	if err := r.open(); err != nil {
		// Keep logging into the renamed file rather than losing records.
		r.f = old
		return err
	}
	old.Close()
	r.prune()
	return nil
}

func (r *RotatingFile) prune() {
	rotated := r.Rotated()
	if len(rotated) <= r.keep {
		return
	}
	for _, p := range rotated[r.keep:] {
		os.Remove(p)
	}
}

// Rotated lists the rotated files, newest first.
func (r *RotatingFile) Rotated() []string {
	matches, _ := filepath.Glob(r.path + ".*")
	var out []string
	for _, m := range matches {
		if rotatedSuffix.MatchString(m[len(r.path):]) {
			out = append(out, m)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

func (r *RotatingFile) report(err error) {
	if r.reported {
		return
	}
	r.reported = true
	_, _ = fmt.Fprintf(r.errOut, "log rotation failed, logging continues in %s: %v\n", r.path, err)
}

func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
