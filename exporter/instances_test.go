package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/patrickdk77/postfix_exporter/exporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postconf -xP output from the batch instance's master.cf.
const batchOverrides = `relay/unix/syslog_name = postfix/relay
yahoo/unix/syslog_name = mail_batch-yahoo
att/unix/syslog_name = mail_batch-att
slow/unix/syslog_name = mail_batch-slow
blacklist/unix/syslog_name = mail_batch-blk
smtp/inet/content_filter = smtp-amavis:[amavis]:10022
`

// fakePostconf writes a postconf stand-in that answers -xh syslog_name
// and -xP and fails any other call. failH and failP make the matching
// call fail.
func fakePostconf(t *testing.T, syslogName, overrides string, failH, failP bool) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	write("h.out", syslogName)
	write("p.out", overrides)
	hExit, pExit := "0", "0"
	if failH {
		hExit = "1"
	}
	if failP {
		pExit = "1"
	}
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"  -xh) cat " + filepath.Join(dir, "h.out") + "; exit " + hExit + " ;;\n" +
		"  -xP) cat " + filepath.Join(dir, "p.out") + "; exit " + pExit + " ;;\n" +
		"esac\nexit 2\n"
	path := filepath.Join(dir, "postconf")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
	return path
}

func TestParseServiceSyslogNames(t *testing.T) {
	assert.Equal(t, []exporter.Instance{
		{SyslogName: "postfix/relay", Service: "relay"},
		{SyslogName: "mail_batch-yahoo", Service: "yahoo"},
		{SyslogName: "mail_batch-att", Service: "att"},
		{SyslogName: "mail_batch-slow", Service: "slow"},
		{SyslogName: "mail_batch-blk", Service: "blacklist"},
	}, parseServiceSyslogNames(batchOverrides))
}

func TestParseServiceSyslogNamesNumericService(t *testing.T) {
	assert.Equal(t, []exporter.Instance{{SyslogName: "mail_in/10051", Service: "10051"}},
		parseServiceSyslogNames("10051/inet/syslog_name = mail_in/10051\n"))
}

func TestParseServiceSyslogNamesIgnoresJunk(t *testing.T) {
	assert.Empty(t, parseServiceSyslogNames("\nno separator here\n/unix/syslog_name = x\nsyslog_name = y\nrelay/unix/syslog_name = \n"))
}

func TestResolveInstancesAuto(t *testing.T) {
	got, err := resolveInstances([]string{"auto"}, fakePostconf(t, "mail_batch\n", batchOverrides, false, false))
	require.NoError(t, err)
	require.Len(t, got, 6)
	assert.Equal(t, exporter.Instance{SyslogName: "mail_batch"}, got[0])
	assert.Equal(t, exporter.Instance{SyslogName: "postfix/relay", Service: "relay"}, got[1])
}

func TestResolveInstancesExplicit(t *testing.T) {
	got, err := resolveInstances([]string{"mail_in", "relay=postfix/relay"}, "/nonexistent/postconf")
	require.NoError(t, err, "explicit names never run postconf")
	assert.Equal(t, []exporter.Instance{{SyslogName: "mail_in"}, {SyslogName: "postfix/relay", Service: "relay"}}, got)
}

func TestResolveInstancesAutoErrors(t *testing.T) {
	for name, postconf := range map[string]string{
		"postconf missing":       "/nonexistent/postconf",
		"syslog_name call fails": fakePostconf(t, "mail_batch\n", batchOverrides, true, false),
		"empty syslog_name":      fakePostconf(t, "\n", batchOverrides, false, false),
		"-xP call fails":         fakePostconf(t, "mail_batch\n", batchOverrides, false, true),
	} {
		_, err := resolveInstances([]string{"auto"}, postconf)
		assert.Error(t, err, name)
	}
}
