package core

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

func TestCheckpointErrorsKeepTheirCauseAndAdvise(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  ErrorCode
		says  string
	}{
		{checkpoint.ErrIndexLocked, CodeBusy, "delete /study/c/.git/index.lock"},
		{checkpoint.ErrRefLocked, CodeBusy, "updating its git branch"},
		{checkpoint.ErrHeadMoved, CodeBusy, "updating its git branch"},
		{checkpoint.ErrWorktreeChanged, CodeBusy, "files kept changing"},
		{checkpoint.ErrRepositoryChanged, CodeCorrupt, "Nothing was committed"},
		{fmt.Errorf("%w: %w", checkpoint.ErrNotRepository, checkpoint.ErrRepositoryChanged), CodeCorrupt, "leads outside the Topic"},
		{fmt.Errorf("%w: /study/c/.git is not a folder", checkpoint.ErrNotRepository), CodeCorrupt, ".git is not a folder"},
		{fmt.Errorf("%w: %w", checkpoint.ErrGitNotFound, exec.ErrNotFound), CodeFailedPrecondition, "install git"},
		{checkpoint.ErrMergeInProgress, CodeFailedPrecondition, "merge is in progress"},
		{checkpoint.ErrNoIdentity, CodeFailedPrecondition, "git config --global user.name"},
		{checkpoint.ErrDetachedHead, CodeFailedPrecondition, "not on a branch"},
		{errors.New("disk full"), CodeInternal, "disk full"},
	} {
		err := checkpointError("c", "/study/c", tc.cause)
		if CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.says) || !errors.Is(err, tc.cause) {
			t.Errorf("%v: got %s %q (cause kept: %v), want %s mentioning %q",
				tc.cause, CodeOf(err), err, errors.Is(err, tc.cause), tc.code, tc.says)
		}
	}
}
