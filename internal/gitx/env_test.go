package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/toshi0607/pgcheckout/internal/config"
)

func TestWriteEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.pgcheckout")
	opts := EnvOptions{Path: new(".env.pgcheckout")}
	const url = "postgres://user:secret@localhost/app__main?sslmode=disable"
	result, err := opts.Write(dir, url)
	if err != nil || result != (EnvResult{EnvFile: path, EnvFileChanged: true}) {
		t.Fatalf("Write = %+v, %v", result, err)
	}
	checkFile(t, path, "# managed by pgcheckout; do not edit\nDATABASE_URL="+url+"\n")
	if info := fileInfo(t, path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}

	oldTime := time.Unix(1700000000, 0)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	before := fileInfo(t, path).ModTime()
	result, err = opts.Write(dir, url)
	if err != nil || result != (EnvResult{EnvFile: path}) {
		t.Fatalf("identical Write = %+v, %v", result, err)
	}
	if after := fileInfo(t, path).ModTime(); !after.Equal(before) {
		t.Errorf("mtime = %v, want %v", after, before)
	}

	opts.Key = new("_BRANCH_URL_2")
	const changedURL = "postgres://localhost/app__next"
	result, err = opts.Write(dir, changedURL)
	if err != nil || result != (EnvResult{EnvFile: path, EnvFileChanged: true}) {
		t.Fatalf("replacement Write = %+v, %v", result, err)
	}
	checkFile(t, path, "# managed by pgcheckout; do not edit\n_BRANCH_URL_2="+changedURL+"\n")
	checkEntries(t, dir, ".env.pgcheckout")
}

func TestWriteEnvRefusesUnmanagedFiles(t *testing.T) {
	for _, tc := range []struct {
		name, content string
	}{
		{"unmanaged", "DATABASE_URL=keep-this\n"},
		{"empty", ""},
		{"header on second line", "\n# managed by pgcheckout; do not edit\n"},
		{"header with extra space", "# managed by pgcheckout; do not edit \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".env")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := (EnvOptions{Path: &path}).Write(dir, "postgres://localhost/app")
			problem, ok := errors.AsType[*config.Error](err)
			if !ok || problem.Code != config.UnmanagedFile || !strings.Contains(problem.Hint, path) {
				t.Fatalf("Write error = %#v, want unmanaged_file with path", err)
			}
			checkFile(t, path, tc.content)
			checkEntries(t, dir, ".env")
		})
	}
}

func TestWriteEnvRejectsInvalidInput(t *testing.T) {
	dir := t.TempDir()
	const url = "postgres://localhost/app"
	opts := EnvOptions{Path: new(".env.pgcheckout")}
	for _, key := range []string{"", "1URL", "DATABASE-URL", "A=B", "URL\n", "あ"} {
		opts.Key = &key
		if _, err := opts.Write(dir, url); config.ErrorCode(err) != config.Usage {
			t.Errorf("Write with key %q: %v, want usage", key, err)
		}
	}
	opts.Key = nil
	for _, invalid := range []string{"\n", "\r", " ", "#", "'", "\"", "\\"} {
		_, err := opts.Write(dir, url+"?param="+invalid)
		problem, ok := errors.AsType[*config.Error](err)
		if !ok || problem.Code != config.ConfigInvalid || !strings.Contains(problem.Hint, "admin URL") {
			t.Errorf("Write with URL character %q: %#v, want config_invalid with admin URL hint", invalid, err)
		}
	}
	checkEntries(t, dir)
}

func TestWriteAtomicCleansUpAfterRenameFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(target, "new content", 0o600); err == nil {
		t.Fatal("writeAtomic succeeded when replacing a directory")
	}
	checkEntries(t, dir, "target")
}

func checkFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Errorf("%s contains %q, want %q", path, data, want)
	}
}

func fileInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func checkEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !slices.Equal(names, want) {
		t.Errorf("%s entries = %v, want %v", dir, names, want)
	}
}
