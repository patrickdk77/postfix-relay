package showq

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func encode(fields ...string) []byte {
	var b bytes.Buffer
	for _, f := range fields {
		b.WriteString(f)
		b.WriteByte(0)
	}
	return b.Bytes()
}

func queueData() []byte {
	now := time.Now()
	return encode(
		"queue_name", "deferred",
		"queue_id", "4AB1",
		"time", fmt.Sprint(now.Add(-100*time.Second).Unix()),
		"size", "1000",
		"forced_expire", "0",
		"sender", "a@example.org",
		"recipient", "r1@example.net",
		"reason", "host mx.example.net[192.0.2.5] said: 450 4.7.1 <r1@example.net>: Recipient address rejected: try later (in reply to RCPT TO command)",
		"recipient", "r2@example.net",
		"reason", "connect to mx.example.net[192.0.2.5]:25: Connection timed out",
		"",
		"queue_name", "active",
		"queue_id", "4AB2",
		"time", fmt.Sprint(now.Add(-5*time.Second).Unix()),
		"size", "2000",
		"forced_expire", "1",
		"sender", "b@example.org",
		"original_recipient", "r3@example.net",
		"recipient", "r3@example.net",
		"log_class", "",
		"reason", "",
		"",
	)
}

func TestRecipientsReasonsAndForcedExpire(t *testing.T) {
	t.Parallel()
	s := NewShowq("")
	s.init()
	require.NoError(t, s.collectBinaryShowqFromScanner(bytes.NewReader(queueData())))

	assert.Equal(t, 2.0, testutil.ToFloat64(s.recipientGauge.WithLabelValues("deferred")))
	assert.Equal(t, 1.0, testutil.ToFloat64(s.recipientGauge.WithLabelValues("active")))
	assert.Equal(t, 0.0, testutil.ToFloat64(s.recipientGauge.WithLabelValues("hold")), "known queues are zero-filled")
	assert.Equal(t, 1.0, testutil.ToFloat64(s.delayedGauge.WithLabelValues("deferred", "450", "4.7.1")))
	assert.Equal(t, 1.0, testutil.ToFloat64(s.delayedGauge.WithLabelValues("deferred", "", "")), "a timeout has no reply code")
	assert.Equal(t, 1.0, testutil.ToFloat64(s.forcedGauge.WithLabelValues("active")))
	assert.Equal(t, 0.0, testutil.ToFloat64(s.forcedGauge.WithLabelValues("deferred")))
}

func TestGaugesResetBetweenScrapes(t *testing.T) {
	t.Parallel()
	s := NewShowq("")
	s.init()
	require.NoError(t, s.collectBinaryShowqFromScanner(bytes.NewReader(queueData())))
	require.NoError(t, s.collectBinaryShowqFromScanner(bytes.NewReader(nil)))
	assert.Equal(t, 0.0, testutil.ToFloat64(s.recipientGauge.WithLabelValues("deferred")))
	assert.Equal(t, 0, testutil.CollectAndCount(s.delayedGauge), "reasons from the last scrape must not linger")
}

func TestMalformedShowqData(t *testing.T) {
	t.Parallel()
	for name, data := range map[string][]byte{
		"key without value": encode("queue_name"),
		"size not a number": encode("queue_name", "active", "size", "big", ""),
		"time not a number": encode("queue_name", "active", "time", "yesterday", ""),
		"missing null byte": append(encode("queue_name"), []byte("active")...),
	} {
		s := NewShowq("")
		s.init()
		assert.Error(t, s.collectBinaryShowqFromScanner(bytes.NewReader(data)), name)
	}
}

func corruptValue(t *testing.T, dir string) (float64, bool) {
	t.Helper()
	s := NewShowq(filepath.Join(t.TempDir(), "no-socket")).WithQueueDirectory(dir)
	ch := make(chan prometheus.Metric, 100)
	assert.Error(t, s.Collect(ch), "there is no showq socket")
	close(ch)
	for m := range ch {
		if m.Desc() == s.corruptGauge.Desc() {
			return testutil.ToFloat64(s.corruptGauge), true
		}
	}
	return 0, false
}

func TestCorruptQueueCount(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "corrupt"), 0o700))
	for _, name := range []string{"4AB1", "4AB2"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "corrupt", name), nil, 0o600))
	}
	v, ok := corruptValue(t, dir)
	assert.True(t, ok, "counted even when showq is down")
	assert.Equal(t, 2.0, v)
}

func TestCorruptQueueMissingCountsZero(t *testing.T) {
	t.Parallel()
	v, ok := corruptValue(t, t.TempDir())
	assert.True(t, ok)
	assert.Equal(t, 0.0, v)
}

func TestCorruptQueueUnreadableIsSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "corrupt"), nil, 0o600))
	_, ok := corruptValue(t, dir)
	assert.False(t, ok, "a corrupt path that is not a directory emits no value")
}

func TestCorruptQueueDisabled(t *testing.T) {
	t.Parallel()
	_, ok := corruptValue(t, "")
	assert.False(t, ok)
}

// ServeShowq answers every connection on a Unix socket with data, like
// Postfix's showq service.
func ServeShowq(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "showq")
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write(data)
			c.Close()
		}
	}()
	return path
}

func TestCollectOverSocket(t *testing.T) {
	t.Parallel()
	s := NewShowq(ServeShowq(t, queueData()))
	ch := make(chan prometheus.Metric, 200)
	require.NoError(t, s.Collect(ch))
	close(ch)
	assert.NotEmpty(t, ch)
	assert.Equal(t, 1.0, testutil.ToFloat64(s.queueMessageGauge.WithLabelValues("deferred")))
	assert.Equal(t, 2.0, testutil.ToFloat64(s.recipientGauge.WithLabelValues("deferred")))
}
