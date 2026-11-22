package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/toshi0607/pgtwig/internal/config"
)

func BranchName(ctx context.Context, cwd string) (string, error) {
	const hint = "Pass a branch name explicitly."
	name, err := gitOutput(ctx, cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		if notGitRepo(err) {
			return "", &config.Error{Code: config.NotGitRepo, Message: "current directory is not in a Git repository",
				Hint: hint, Cause: err}
		}
		return "", fmt.Errorf("resolve Git branch: %w", err)
	}
	if name == "HEAD" {
		return "", config.NewError(config.DetachedHead, "Git HEAD is detached", hint)
	}
	return name, nil
}

func gitOutput(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\r\n"), err
}

func notGitRepo(err error) bool {
	exit, ok := errors.AsType[*exec.ExitError](err)
	return ok && bytes.Contains(exit.Stderr, []byte("not a git repository"))
}
