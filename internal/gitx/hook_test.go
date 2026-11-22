package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/toshi0607/pgtwig/internal/config"
)

func TestInstallHook(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  EnvOptions
		args string
	}{
		{"no arguments", EnvOptions{}, ""},
		{"env file", EnvOptions{Path: new("dir with spaces/.env")}, " --env-file 'dir with spaces/.env'"},
		{"env file and key", EnvOptions{Path: new("./.env"), Key: new("BRANCH_URL")}, " --env-file './.env' --env-key 'BRANCH_URL'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, path := initRepo(t)
			want := InstallResult{Hook: path, Installed: true, Changed: true}
			result, err := InstallHook(t.Context(), dir, tc.env, false)
			if err != nil || result != want {
				t.Fatalf("InstallHook = %+v, %v, want %+v", result, err, want)
			}
			checkFile(t, path, `#!/bin/sh
# managed by pgtwig; do not edit
# $3 is 1 for a branch checkout, 0 for a file checkout.
[ "$3" = "1" ] || exit 0
command -v pgtwig >/dev/null 2>&1 || exit 0
pgtwig ensure`+tc.args+` >/dev/null || echo "pgtwig: ensure failed; the branch database was not switched" >&2
exit 0
`)
			if info := fileInfo(t, path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
				t.Errorf("mode = %o, want 755", info.Mode().Perm())
			}
			want.Changed = false
			result, err = InstallHook(t.Context(), dir, tc.env, false)
			if err != nil || result != want {
				t.Errorf("second InstallHook = %+v, %v, want %+v", result, err, want)
			}
			checkEntries(t, filepath.Dir(path), "post-checkout")
		})
	}
}

func TestInstallHookForce(t *testing.T) {
	dir, path := initRepo(t)
	const original = "#!/bin/sh\necho original\n"
	const replacement = "#!/bin/sh\necho replacement\n"
	writeTestHook(t, path, original)

	_, err := InstallHook(t.Context(), dir, EnvOptions{}, false)
	if config.ErrorCode(err) != config.UnmanagedFile {
		t.Fatalf("InstallHook = %v, want unmanaged_file", err)
	}
	checkFile(t, path, original)
	checkEntries(t, filepath.Dir(path), "post-checkout")

	backup := path + ".pgtwig-backup"
	want := InstallResult{Hook: path, Installed: true, Changed: true, Backup: backup}
	result, err := InstallHook(t.Context(), dir, EnvOptions{}, true)
	if err != nil || result != want {
		t.Fatalf("forced InstallHook = %+v, %v, want %+v", result, err, want)
	}
	checkFile(t, backup, original)

	result, err = InstallHook(t.Context(), dir, EnvOptions{Path: new(".env")}, true)
	if err != nil || result != (InstallResult{Hook: path, Installed: true, Changed: true}) {
		t.Fatalf("forced update of managed hook = %+v, %v", result, err)
	}
	checkFile(t, backup, original)

	writeTestHook(t, path, replacement)
	_, err = InstallHook(t.Context(), dir, EnvOptions{}, true)
	if config.ErrorCode(err) != config.UnmanagedFile {
		t.Fatalf("forced InstallHook with existing backup = %v, want unmanaged_file", err)
	}
	checkFile(t, path, replacement)
	checkFile(t, backup, original)
	checkEntries(t, filepath.Dir(path), "post-checkout", "post-checkout.pgtwig-backup")
}

func TestUninstallHook(t *testing.T) {
	dir, path := initRepo(t)
	want := UninstallResult{Hook: path}
	result, err := UninstallHook(t.Context(), dir)
	if err != nil || result != want {
		t.Fatalf("missing UninstallHook = %+v, %v, want %+v", result, err, want)
	}

	const original = "#!/bin/sh\n# unmanaged\n# managed by pgtwig; do not edit\n"
	writeTestHook(t, path, original)
	if _, err := UninstallHook(t.Context(), dir); config.ErrorCode(err) != config.UnmanagedFile {
		t.Fatalf("unmanaged UninstallHook = %v, want unmanaged_file", err)
	}
	checkFile(t, path, original)
	if _, err := InstallHook(t.Context(), dir, EnvOptions{}, true); err != nil {
		t.Fatal(err)
	}

	want.Removed = true
	want.Backup = path + ".pgtwig-backup"
	result, err = UninstallHook(t.Context(), dir)
	if err != nil || result != want {
		t.Fatalf("managed UninstallHook = %+v, %v, want %+v", result, err, want)
	}
	checkEntries(t, filepath.Dir(path), "post-checkout.pgtwig-backup")
	checkFile(t, want.Backup, original)

	want.Removed = false
	result, err = UninstallHook(t.Context(), dir)
	if err != nil || result != want {
		t.Errorf("missing UninstallHook with backup = %+v, %v, want %+v", result, err, want)
	}
}

func TestInstallHookWithConfiguredHooksPath(t *testing.T) {
	dir, path := initRepo(t)
	configured := t.TempDir()
	git(t, dir, "config", "--local", "core.hooksPath", configured)
	want := InstallResult{Hook: path, Installed: true, Changed: true,
		Note: "core.hooksPath is set to " + configured + "; the hook runs only if that hooks path chains to this repository's local hooks"}
	result, err := InstallHook(t.Context(), dir, EnvOptions{}, false)
	if err != nil || result != want {
		t.Fatalf("InstallHook = %+v, %v, want %+v", result, err, want)
	}
	checkEntries(t, filepath.Dir(path), "post-checkout")
	checkEntries(t, configured)
}

func TestInstallHookRefusesConfiguredDirectoryAsTarget(t *testing.T) {
	dir, path := initRepo(t)
	git(t, dir, "config", "--local", "core.hooksPath", ".git/hooks")
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHook(t.Context(), subdir, EnvOptions{}, false); err == nil {
		t.Fatal("InstallHook succeeded in the configured directory")
	}
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("configured hooks directory was created: %v", err)
	}
}

func TestInstallHookInLinkedWorktree(t *testing.T) {
	dir, path := initRepo(t)
	git(t, dir, "config", "--local", "user.name", "pgtwig test")
	git(t, dir, "config", "--local", "user.email", "pgtwig@example.invalid")
	git(t, dir, "-c", "core.hooksPath=", "commit", "--allow-empty", "-m", "initial")
	worktree := filepath.Join(t.TempDir(), "linked")
	git(t, dir, "-c", "core.hooksPath=", "worktree", "add", "-b", "linked", worktree)
	result, err := InstallHook(t.Context(), worktree, EnvOptions{}, false)
	if err != nil || !result.Installed || !result.Changed {
		t.Fatalf("InstallHook = %+v, %v", result, err)
	}
	if !os.SameFile(fileInfo(t, result.Hook), fileInfo(t, path)) {
		t.Errorf("hook path = %q, want common hook %q", result.Hook, path)
	}
}

func TestHookRequiresWorkTree(t *testing.T) {
	isolateGitConfig(t)
	dir := t.TempDir()
	git(t, dir, "init", "--bare", "--template=", "bare")
	for _, cwd := range []string{dir, filepath.Join(dir, "bare")} {
		if _, err := InstallHook(t.Context(), cwd, EnvOptions{}, false); config.ErrorCode(err) != config.NotGitRepo {
			t.Errorf("InstallHook(%q) = %v, want not_git_repo", cwd, err)
		}
		if _, err := UninstallHook(t.Context(), cwd); config.ErrorCode(err) != config.NotGitRepo {
			t.Errorf("UninstallHook(%q) = %v, want not_git_repo", cwd, err)
		}
	}
}

func TestInstallHookRejectsUnsafePaths(t *testing.T) {
	dir, path := initRepo(t)
	for _, invalid := range []string{"a'b", "a\nb"} {
		if _, err := InstallHook(t.Context(), dir, EnvOptions{Path: &invalid}, false); config.ErrorCode(err) != config.Usage {
			t.Errorf("InstallHook with path %q = %v, want usage", invalid, err)
		}
	}
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("hooks directory was created: %v", err)
	}
}

func initRepo(t *testing.T) (dir, hook string) {
	t.Helper()
	isolateGitConfig(t)
	dir = t.TempDir()
	git(t, dir, "init", "--template=")
	return dir, filepath.Join(dir, ".git", "hooks", "post-checkout")
}

func isolateGitConfig(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeTestHook(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
