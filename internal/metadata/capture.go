package metadata

import (
	"errors"
	"io"
)

const (
	DefaultHeadCapture = 1 << 20
	DefaultTailCapture = 1 << 20
)

var errRangeNotCaptured = errors.New("metadata: byte range was not captured")

// Capture is an io.Writer that keeps the first and last bytes of a stream, so metadata can be extracted from
// content that is already streamed for hashing without reading it again.
type Capture struct {
	headLimit int
	tailLimit int
	head      []byte
	tail      []byte
	size      int64
}

func NewCapture() *Capture {
	return NewCaptureWithLimits(DefaultHeadCapture, DefaultTailCapture)
}

func NewCaptureWithLimits(headLimit, tailLimit int) *Capture {
	return &Capture{headLimit: headLimit, tailLimit: tailLimit}
}

func (c *Capture) Write(p []byte) (int, error) {
	n := len(p)
	c.size += int64(n)
	if room := c.headLimit - len(c.head); room > 0 {
		take := min(room, len(p))
		c.head = append(c.head, p[:take]...)
		p = p[take:]
	}
	if len(p) == 0 || c.tailLimit <= 0 {
		return n, nil
	}
	c.tail = append(c.tail, p...)
	if len(c.tail) > 2*c.tailLimit {
		c.tail = append(c.tail[:0], c.tail[len(c.tail)-c.tailLimit:]...)
	}
	return n, nil
}

// Size returns the number of bytes written.
func (c *Capture) Size() int64 {
	return c.size
}

// ReaderAt returns a reader over the captured bytes. Reads from the uncaptured middle of the stream fail.
func (c *Capture) ReaderAt() io.ReaderAt {
	tail := c.tail
	if len(tail) > c.tailLimit {
		tail = tail[len(tail)-c.tailLimit:]
	}
	return &sparseReader{head: c.head, tail: tail, tailStart: c.size - int64(len(tail)), size: c.size}
}

type sparseReader struct {
	head      []byte
	tail      []byte
	tailStart int64
	size      int64
}

func (r *sparseReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errRangeNotCaptured
	}
	n := 0
	for n < len(p) {
		pos := off + int64(n)
		if pos >= r.size {
			return n, io.EOF
		}
		switch {
		case pos < int64(len(r.head)):
			n += copy(p[n:], r.head[pos:])
		case pos >= r.tailStart:
			n += copy(p[n:], r.tail[pos-r.tailStart:])
		default:
			return n, errRangeNotCaptured
		}
	}
	return n, nil
}
