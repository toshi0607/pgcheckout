package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/toshi0607/pgtwig/internal/config"
)

const hookMode = 0o755

type InstallResult struct {
	Hook      string `json:"hook"`
	Installed bool   `json:"installed"`
	Changed   bool   `json:"changed"`
	Backup    string `json:"backup,omitempty"`
	Note      string `json:"note,omitempty"`
}

type UninstallResult struct {
	Hook    string `json:"hook"`
	Removed bool   `json:"removed"`
	Backup  string `json:"backup,omitempty"`
}

func InstallHook(ctx context.Context, cwd string, env EnvOptions, force bool) (InstallResult, error) {
	if err := env.Validate(); err != nil {
		return InstallResult{}, err
	}
	args := ""
	if env.Path != nil {
		if strings.ContainsAny(*env.Path, "'\r\n") {
			return InstallResult{}, config.NewError(config.Usage, "hook --env-file path must not contain a single quote or newline", "")
		}
		args = " --env-file '" + *env.Path + "'"
		if env.Key != nil {
			args += " --env-key '" + *env.Key + "'"
		}
	}
	content := "#!/bin/sh\n" + managedHeader + "\n" + `# $3 is 1 for a branch checkout, 0 for a file checkout.
[ "$3" = "1" ] || exit 0
command -v pgtwig >/dev/null 2>&1 || exit 0
pgtwig ensure` + args + ` >/dev/null || echo "pgtwig: ensure failed; the branch database was not switched" >&2
exit 0
`
	path, err := hookPath(ctx, cwd)
	if err != nil {
		return InstallResult{}, err
	}
	result := InstallResult{Hook: path, Installed: true}
	configured, err := configuredHooksPath(ctx, cwd)
	if err != nil {
		return InstallResult{}, err
	}
	if configured != "" {
		result.Note = fmt.Sprintf("core.hooksPath is set to %s; the hook runs only if that hooks path chains to this repository's local hooks", configured)
	}
	data, info, err := readFile(path)
	if err != nil {
		return InstallResult{}, fmt.Errorf("read post-checkout hook: %w", err)
	}
	unmanaged := info != nil && !managedHook(data)
	if unmanaged && !force {
		return InstallResult{}, unmanagedFile(path)
	}
	if info != nil && string(data) == content && (runtime.GOOS == "windows" || info.Mode().Perm() == hookMode) {
		return result, nil
	}
	dir := filepath.Dir(path)
	if err := checkHooksPath(ctx, cwd, dir, configured); err != nil {
		return InstallResult{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return InstallResult{}, fmt.Errorf("create local hooks directory: %w", err)
	}
	if unmanaged {
		result.Backup = path + ".pgtwig-backup"
		if err := backupHook(path, result.Backup); err != nil {
			return InstallResult{}, err
		}
	}
	if err := writeAtomic(path, content, hookMode); err != nil {
		hint := ""
		if result.Backup != "" {
			hint = fmt.Sprintf("The previous hook was saved at %q.", result.Backup)
		}
		return InstallResult{}, &config.Error{Code: config.Internal, Message: "could not install post-checkout hook", Hint: hint, Cause: err}
	}
	result.Changed = true
	return result, nil
}

func UninstallHook(ctx context.Context, cwd string) (UninstallResult, error) {
	path, err := hookPath(ctx, cwd)
	if err != nil {
		return UninstallResult{}, err
	}
	result := UninstallResult{Hook: path}
	backup := path + ".pgtwig-backup"
	if _, err := os.Lstat(backup); err == nil {
		result.Backup = backup
	} else if !errors.Is(err, os.ErrNotExist) {
		return UninstallResult{}, fmt.Errorf("check post-checkout backup: %w", err)
	}
	data, info, err := readFile(path)
	if err != nil {
		return UninstallResult{}, fmt.Errorf("read post-checkout hook: %w", err)
	}
	if info == nil {
		return result, nil
	}
	if !managedHook(data) {
		return UninstallResult{}, unmanagedFile(path)
	}
	configured, err := configuredHooksPath(ctx, cwd)
	if err != nil {
		return UninstallResult{}, err
	}
	if err := checkHooksPath(ctx, cwd, filepath.Dir(path), configured); err != nil {
		return UninstallResult{}, err
	}
	if err := os.Remove(path); err != nil {
		return UninstallResult{}, fmt.Errorf("remove post-checkout hook: %w", err)
	}
	result.Removed = true
	return result, nil
}

func managedHook(data []byte) bool {
	_, rest, _ := strings.Cut(string(data), "\n")
	marker, _, _ := strings.Cut(rest, "\n")
	return marker == managedHeader
}

func backupHook(path, backup string) (err error) {
	// Reserve the backup name before rename so concurrent installs cannot clobber it.
	file, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return config.NewError(config.UnmanagedFile, "post-checkout backup already exists", fmt.Sprintf("Move %q out of the way and retry.", backup))
	}
	if err != nil {
		return fmt.Errorf("reserve post-checkout backup: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(backup))
		}
	}()
	if err := file.Close(); err != nil {
		return fmt.Errorf("close post-checkout backup reservation: %w", err)
	}
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("back up post-checkout hook: %w", err)
	}
	return nil
}

func hookPath(ctx context.Context, cwd string) (string, error) {
	out, err := gitOutput(ctx, cwd, "rev-parse", "--is-inside-work-tree", "--git-common-dir")
	inside, common, _ := strings.Cut(out, "\n")
	if notGitRepo(err) || (err == nil && strings.TrimSuffix(inside, "\r") != "true") {
		return "", &config.Error{Code: config.NotGitRepo, Message: "current directory is not in a Git work tree", Cause: err}
	}
	if err != nil {
		return "", fmt.Errorf("resolve Git common directory: %w", err)
	}
	dir, err := absolutePath(cwd, common)
	if err != nil {
		return "", fmt.Errorf("resolve local hooks directory: %w", err)
	}
	return filepath.Join(dir, "hooks", "post-checkout"), nil
}

func configuredHooksPath(ctx context.Context, cwd string) (string, error) {
	path, err := gitOutput(ctx, cwd, "config", "--get", "core.hooksPath")
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 && len(exit.Stderr) == 0 {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read core.hooksPath: %w", err)
	}
	return path, nil
}

func checkHooksPath(ctx context.Context, cwd, local, configured string) error {
	if configured == "" {
		return nil
	}
	if !filepath.IsAbs(configured) {
		root, err := gitOutput(ctx, cwd, "rev-parse", "--show-toplevel")
		if err != nil {
			return fmt.Errorf("resolve work tree root: %w", err)
		}
		configured = filepath.Join(root, configured)
	}
	if filepath.Clean(configured) != local {
		localDir := local
		localInfo, err := os.Stat(localDir)
		if errors.Is(err, os.ErrNotExist) {
			if filepath.Base(configured) != filepath.Base(localDir) {
				return nil
			}
			// A directory that does not exist yet can still have an aliased parent.
			localDir, configured = filepath.Dir(localDir), filepath.Dir(configured)
			localInfo, err = os.Stat(localDir)
		}
		if err != nil {
			return fmt.Errorf("check local hooks directory: %w", err)
		}
		configuredInfo, err := os.Stat(configured)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("check configured hooks directory: %w", err)
		}
		if !os.SameFile(localInfo, configuredInfo) {
			return nil
		}
	}
	return config.NewError(config.Internal, "local hooks directory is also the configured core.hooksPath",
		fmt.Sprintf("The hook directory %q is reserved by core.hooksPath.", local))
}
