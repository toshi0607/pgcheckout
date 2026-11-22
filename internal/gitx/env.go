package gitx

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/toshi0607/pgcheckout/internal/config"
)

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type EnvOptions struct {
	Path *string
	Key  *string
}

func (o EnvOptions) Validate() error {
	switch {
	case o.Key != nil && o.Path == nil:
		return config.NewError(config.Usage, "--env-key requires --env-file", "")
	case o.Path != nil && *o.Path == "":
		return config.NewError(config.Usage, "--env-file must not be empty", "")
	case o.Key != nil && !envKeyPattern.MatchString(*o.Key):
		return config.NewError(config.Usage, "--env-key must match [A-Za-z_][A-Za-z0-9_]*", "")
	}
	return nil
}

type EnvResult struct {
	EnvFile        string `json:"env_file"`
	EnvFileChanged bool   `json:"env_file_changed"`
}

func (o EnvOptions) Write(cwd, url string) (EnvResult, error) {
	if err := o.Validate(); err != nil {
		return EnvResult{}, err
	}
	if o.Path == nil {
		return EnvResult{}, config.NewError(config.Usage, "--env-file is required", "")
	}
	if strings.ContainsAny(url, "\r\n #'\"\\") {
		return EnvResult{}, config.NewError(config.ConfigInvalid, "branch URL contains characters that cannot be written to an env file",
			"Use an admin URL without newlines, spaces, #, quotes, or backslashes.")
	}
	path, err := absolutePath(cwd, *o.Path)
	if err != nil {
		return EnvResult{}, fmt.Errorf("resolve env file: %w", err)
	}
	key := "DATABASE_URL"
	if o.Key != nil {
		key = *o.Key
	}
	content := managedHeader + "\n" + key + "=" + url + "\n"
	data, info, err := readFile(path)
	if err != nil {
		return EnvResult{}, fmt.Errorf("read env file: %w", err)
	}
	result := EnvResult{EnvFile: path}
	if info != nil {
		header, _, _ := strings.Cut(string(data), "\n")
		if header != managedHeader {
			return EnvResult{}, unmanagedFile(path)
		}
		if string(data) == content {
			return result, nil
		}
	}
	if err := writeAtomic(path, content, 0o600); err != nil {
		return EnvResult{}, fmt.Errorf("write env file: %w", err)
	}
	result.EnvFileChanged = true
	return result, nil
}
