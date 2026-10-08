package logsource

import (
	"bufio"
	"context"
	"io"
	"strings"
	"sync"
)

// A StdinLogSource reads newline-terminated log lines from a stream,
// such as the pipe rsyslog's omprog module writes to.
type StdinLogSource struct {
	lines     chan string
	done      chan struct{}
	closeOnce sync.Once
	err       error
	path      string
	LogSourceDefaults
}

// NewStdinLogSource starts reading lines from r. The returned source
// reports path as its location.
func NewStdinLogSource(r io.Reader, path string) *StdinLogSource {
	s := &StdinLogSource{
		lines: make(chan string),
		done:  make(chan struct{}),
		path:  path,
	}
	go s.run(r)
	return s
}

func (s *StdinLogSource) run(r io.Reader) {
	defer close(s.lines)
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line = strings.TrimRight(line, "\r\n"); line != "" {
			select {
			case s.lines <- line:
			case <-s.done:
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				s.err = err
			}
			return
		}
	}
}

func (s *StdinLogSource) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *StdinLogSource) Path() string {
	return s.path
}

// Read returns the next line, or io.EOF once the writer closes the
// stream or the source is closed.
func (s *StdinLogSource) Read(ctx context.Context) (string, error) {
	select {
	case <-s.done:
		return "", io.EOF
	case line, ok := <-s.lines:
		if !ok {
			if s.err != nil {
				return "", s.err
			}
			return "", io.EOF
		}
		return line, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
