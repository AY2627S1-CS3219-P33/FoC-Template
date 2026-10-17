package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLatestVersionReadsEmbeddedFiles(t *testing.T) {
	version, err := LatestVersion()
	require.NoError(t, err)
	require.GreaterOrEqual(t, version, int64(1))
}
