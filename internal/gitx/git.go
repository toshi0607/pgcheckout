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
	name, err := gitOutput(ctx, cwd, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if notGitRepo(err) {
			return "", &config.Error{Code: config.NotGitRepo, Message: gitStderr(err),
				Hint: hint, Cause: err}
		}
		if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 && name == "" {
			message := "Git HEAD is detached"
			if diagnostic := gitStderr(err); diagnostic != "" {
				message += ": " + diagnostic
			}
			return "", &config.Error{Code: config.DetachedHead, Message: message, Hint: hint, Cause: err}
		}
		return "", fmt.Errorf("resolve Git branch: %w", err)
	}
	return name, nil
}

func gitOutput(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		switch {
		case ctx.Err() != nil:
			err = &config.Error{Code: config.Canceled, Message: "Git operation canceled", Cause: errors.Join(ctx.Err(), err)}
		case gitStderr(err) != "":
			err = &config.Error{Code: config.Internal, Message: gitStderr(err), Cause: err}
		}
	}
	return strings.TrimRight(string(out), "\r\n"), err
}

func gitStderr(err error) string {
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		line, _, _ := bytes.Cut(exit.Stderr, []byte("\n"))
		return config.RedactConnectionDetails(strings.TrimSpace(string(line)))
	}
	return ""
}

func notGitRepo(err error) bool {
	exit, ok := errors.AsType[*exec.ExitError](err)
	return ok && bytes.Contains(exit.Stderr, []byte("not a git repository"))
}
