package gitx

import (
	"strings"
	"testing"

	"github.com/toshi0607/pgtwig/internal/config"
)

func TestBranchName(t *testing.T) {
	isolateGitConfig(t)
	dir := t.TempDir()
	if _, err := BranchName(t.Context(), dir); config.ErrorCode(err) != config.NotGitRepo ||
		!strings.Contains(err.Error(), "fatal: not a git repository") {
		t.Fatalf("BranchName outside a repo = %v, want not_git_repo with Git's diagnostic", err)
	}
	git(t, dir, "init", "--template=", "--initial-branch=topic")
	if got, err := BranchName(t.Context(), dir); err != nil || got != "topic" {
		t.Fatalf("unborn branch = %q, %v", got, err)
	}
	git(t, dir, "-c", "user.name=pgtwig test", "-c", "user.email=pgtwig@example.invalid", "commit", "--allow-empty", "-m", "initial")
	git(t, dir, "checkout", "--detach", "HEAD")
	if _, err := BranchName(t.Context(), dir); config.ErrorCode(err) != config.DetachedHead {
		t.Fatalf("detached HEAD = %v, want detached_head", err)
	}
}
