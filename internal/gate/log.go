package gate

import (
	"bytes"
	"io"
	"sync"
)

// tagged writes one source's output to a log the sources share, a whole
// line at a time and each line prefixed with the source's runners, so the
// lines of sources running at once stay whole and attributable.
type tagged struct {
	mu     *sync.Mutex // shared by every writer of dst
	dst    io.Writer
	prefix []byte
	buf    []byte
}

func (t *tagged) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		if err := t.emit(t.buf[:i+1]); err != nil {
			return len(p), err
		}
		t.buf = t.buf[i+1:]
	}
}

// flush writes a final line the source left unterminated.
func (t *tagged) flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.buf) > 0 {
		_ = t.emit(append(t.buf, '\n'))
		t.buf = nil
	}
}

func (t *tagged) emit(line []byte) error {
	_, err := t.dst.Write(append(append([]byte(nil), t.prefix...), line...))
	return err
}
