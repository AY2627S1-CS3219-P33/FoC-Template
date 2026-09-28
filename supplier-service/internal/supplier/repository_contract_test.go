package supplier

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeletionStateContract(t *testing.T) {
	require.Equal(t, DeletionState("requested"), DeletionRequested)
	require.Equal(t, DeletionState("release_pending"), DeletionReleasePending)
	require.Equal(t, DeletionState("commit_pending"), DeletionCommitPending)
	require.Equal(t, DeletionState("rejected_active_errands"), DeletionRejectedActiveErrands)
	require.Equal(t, DeletionState("completed"), DeletionCompleted)
}
