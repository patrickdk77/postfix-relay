package logsource

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readAll(t *testing.T, s *StdinLogSource) ([]string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var lines []string
	for {
		line, err := s.Read(ctx)
		if err != nil {
			return lines, err
		}
		lines = append(lines, line)
	}
}

func TestStdinLogSourceReadsLinesUntilEOF(t *testing.T) {
	t.Parallel()
	s := NewStdinLogSource(strings.NewReader("first\r\n\nsecond\nlast without newline"), "stdin")
	lines, err := readAll(t, s)
	assert.ErrorIs(t, err, io.EOF)
	assert.Equal(t, []string{"first", "second", "last without newline"}, lines)
	assert.Equal(t, "stdin", s.Path())
}

func TestStdinLogSourceEmptyInput(t *testing.T) {
	t.Parallel()
	lines, err := readAll(t, NewStdinLogSource(strings.NewReader(""), "stdin"))
	assert.ErrorIs(t, err, io.EOF)
	assert.Empty(t, lines)
}

type failingReader struct {
	data string
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestStdinLogSourceReturnsReadError(t *testing.T) {
	t.Parallel()
	readErr := errors.New("pipe broke")
	lines, err := readAll(t, NewStdinLogSource(&failingReader{data: "one\n", err: readErr}, "stdin"))
	assert.ErrorIs(t, err, readErr)
	assert.Equal(t, []string{"one"}, lines)
}

func TestStdinLogSourceReadHonoursContext(t *testing.T) {
	t.Parallel()
	r, w := io.Pipe()
	defer w.Close()
	s := NewStdinLogSource(r, "stdin")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Read(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestStdinLogSourceCloseStopsReader(t *testing.T) {
	t.Parallel()
	r, w := io.Pipe()
	s := NewStdinLogSource(r, "stdin")
	go func() { _, _ = w.Write([]byte("unread line\n")) }()
	require.NoError(t, s.Close())
	require.NoError(t, s.Close(), "second Close must not panic")
	_, err := readAll(t, s)
	assert.ErrorIs(t, err, io.EOF, "the reader goroutine exits once closed")
	_ = w.Close()
}

func newFileFactory(t *testing.T, args ...string) *fileLogSourceFactory {
	t.Helper()
	app := kingpin.New("test", "")
	f := &fileLogSourceFactory{}
	f.Init(app)
	_, err := app.Parse(args)
	require.NoError(t, err)
	return f
}

func TestFileFactorySelectsStdin(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"-", "/dev/stdin"} {
		srcs, err := newFileFactory(t, "--postfix.logfile_path="+path).New(context.Background())
		require.NoError(t, err)
		require.Len(t, srcs, 1)
		assert.IsType(t, &StdinLogSource{}, srcs[0], path)
		assert.False(t, (&fileLogSourceFactory{}).Watchdog(context.Background()))
	}
}

func TestFileFactoryWithoutPathConfiguresNothing(t *testing.T) {
	t.Parallel()
	srcs, err := newFileFactory(t).New(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, srcs, "no path means showq metrics only")
}

func TestFileFactoryMissingFileFails(t *testing.T) {
	t.Parallel()
	_, err := newFileFactory(t, "--postfix.logfile_path=/nonexistent/mail.log").New(context.Background())
	assert.Error(t, err)
}
