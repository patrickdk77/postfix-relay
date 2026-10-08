package exporter

import (
	"bytes"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrickdk77/postfix_exporter/showq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serveShowq(t *testing.T, fields ...string) string {
	t.Helper()
	var b bytes.Buffer
	for _, f := range fields {
		b.WriteString(f)
		b.WriteByte(0)
	}
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
			_, _ = c.Write(b.Bytes())
			c.Close()
		}
	}()
	return path
}

func TestShowqOnlyExporter(t *testing.T) {
	t.Parallel()
	path := serveShowq(t, "queue_name", "deferred", "size", "100", "time", "1", "recipient", "a@example.net", "")
	e := NewPostfixExporter(showq.NewShowq(path), nil, false)
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(e))

	families, err := reg.Gather()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, f := range families {
		names[f.GetName()] = true
	}
	assert.True(t, names["postfix_showq_queue_depth"])
	assert.True(t, names["postfix_showq_queue_recipients"])
	assert.False(t, names["postfix_smtpd_connects_total"], "no log metrics without a log source")
	assert.False(t, names["postfix_log_entries_total"])
	assert.Equal(t, 1.0, testutil.ToFloat64(e.postfixUp.WithLabelValues("unix://"+path)))
}

func TestShowqOnlyExporterSocketDown(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing")
	e := NewPostfixExporter(showq.NewShowq(path), nil, false)
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(e))

	err := testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP postfix_up Whether scraping Postfix's metrics was successful.
# TYPE postfix_up gauge
postfix_up{path="unix://`+path+`"} 0
`), "postfix_up")
	assert.NoError(t, err)
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		assert.NotContains(t, f.GetName(), "showq_", "no queue metrics when showq is unreachable")
	}
}
