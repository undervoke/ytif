package checkrun

import (
	"bytes"
	"io"
	"sync"
)

// lineWriter keeps concurrently streamed output attributable to its command.
type lineWriter struct {
	mu     *sync.Mutex
	dst    io.Writer
	prefix string
	buf    []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		if _, err := w.dst.Write(append([]byte(w.prefix), w.buf[:i+1]...)); err != nil {
			return len(p), err
		}
		w.buf = w.buf[i+1:]
	}
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		_, _ = w.dst.Write(append(append([]byte(w.prefix), w.buf...), '\n'))
		w.buf = nil
	}
}
