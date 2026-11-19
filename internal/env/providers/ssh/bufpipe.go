package ssh

import (
	"io"
	"sync"
)

// bufPipe is an in-memory pipe whose writer only blocks once maxBuf bytes are
// pending, so a consumer that ignores one stream (e.g. stderr) does not stall the other.
type bufPipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	err    error // set by CloseWithError; returned to reader after buf drained
	rdone  bool  // reader closed
	maxBuf int
}

func newBufPipe(max int) *bufPipe {
	p := &bufPipe{maxBuf: max}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *bufPipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for len(b) > 0 {
		for !p.rdone && p.err == nil && len(p.buf) >= p.maxBuf {
			p.cond.Wait()
		}
		if p.rdone {
			return n, io.ErrClosedPipe
		}
		if p.err != nil {
			return n, io.ErrClosedPipe
		}
		k := min(len(b), p.maxBuf-len(p.buf))
		p.buf = append(p.buf, b[:k]...)
		b, n = b[k:], n+k
		p.cond.Broadcast()
	}
	return n, nil
}

func (p *bufPipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) == 0 && p.err == nil && !p.rdone {
		p.cond.Wait()
	}
	if len(p.buf) == 0 {
		if p.rdone {
			return 0, io.ErrClosedPipe
		}
		return 0, p.err
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	if len(p.buf) == 0 {
		p.buf = nil
	}
	p.cond.Broadcast()
	return n, nil
}

// CloseWithError ends the write side; nil means io.EOF.
func (p *bufPipe) CloseWithError(err error) {
	if err == nil {
		err = io.EOF
	}
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	p.cond.Broadcast()
	p.mu.Unlock()
}

// Close closes the read side.
func (p *bufPipe) Close() error {
	p.mu.Lock()
	p.rdone = true
	p.buf = nil
	p.cond.Broadcast()
	p.mu.Unlock()
	return nil
}
