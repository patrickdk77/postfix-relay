package logsource

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileFactoryMissingFileOptionalMeansShowqOnly(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "mail.log")
	srcs, err := newFileFactory(t, "--postfix.logfile_path="+path, "--no-postfix.logfile_must_exist").New(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, srcs, "a missing optional log file runs on showq alone")
}

func TestFileFactoryExistingFileOptionalIsTailed(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "mail.log")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	srcs, err := newFileFactory(t, "--postfix.logfile_path="+path, "--no-postfix.logfile_must_exist").New(context.Background())
	require.NoError(t, err)
	require.Len(t, srcs, 1)
	assert.IsType(t, &FileLogSource{}, srcs[0])
	assert.Equal(t, path, srcs[0].Path())
	require.NoError(t, srcs[0].Close())
}

func TestFileFactoryOptionalStillFailsOnOtherErrors(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads through directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mail.log"), nil, 0o600))
	require.NoError(t, os.Chmod(dir, 0))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_, err := newFileFactory(t, "--postfix.logfile_path="+filepath.Join(dir, "mail.log"), "--no-postfix.logfile_must_exist").New(context.Background())
	assert.ErrorIs(t, err, os.ErrPermission)
}
