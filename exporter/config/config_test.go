package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "postfix.yml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadValidConfig(t *testing.T) {
	t.Parallel()
	cfg, err := Load(write(t, `
status_replies:
  - statuses: [deferred]
    not_statuses: [sent]
    regexp: (?i)try again
    text: retry
smtp_replies:
  - regexp: ^4
    match: code
    text: temporary
reject_replies:
  - regexp: ^5\.7\.1$
    match: enhanced_code
    text: policy
`))
	require.NoError(t, err)
	require.Len(t, cfg.StatusReplies, 1)
	assert.True(t, cfg.StatusReplies[0].AppliesTo("deferred"))
	assert.False(t, cfg.StatusReplies[0].AppliesTo("bounced"))
	assert.Equal(t, MatchTypeCode, cfg.SmtpReplies[0].Match)
	assert.Equal(t, MatchTypeEnhancedCode, cfg.RejectReplies[0].Match)
	assert.Equal(t, MatchTypeText, cfg.StatusReplies[0].Match)
}

func TestNotStatusesExcludes(t *testing.T) {
	t.Parallel()
	cfg, err := Load(write(t, "status_replies:\n  - not_statuses: [sent]\n    regexp: .\n    text: x\n"))
	require.NoError(t, err)
	assert.False(t, cfg.StatusReplies[0].AppliesTo("sent"))
	assert.True(t, cfg.StatusReplies[0].AppliesTo("bounced"))
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"not yaml":            "reject_replies: [",
		"unknown field":       "noqueue_reject_replies:\n  - regexp: x\n    text: y\n",
		"missing regexp":      "reject_replies:\n  - text: y\n",
		"empty text":          "smtp_replies:\n  - regexp: x\n",
		"status without text": "status_replies:\n  - regexp: x\n",
		"bad match type":      "reject_replies:\n  - regexp: x\n    match: subject\n    text: y\n",
		"invalid regexp":      "reject_replies:\n  - regexp: (unclosed\n    text: y\n",
	} {
		_, err := Load(write(t, body))
		assert.Error(t, err, name)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join(t.TempDir(), "absent.yml"))
	assert.ErrorContains(t, err, "error reading config file")
}
