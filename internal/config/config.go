package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	AdminURL *url.URL
	Base     string
	PoolSize int
	Strategy string
}

type Overrides struct {
	AdminURL *string
	Base     *string
	PoolSize *string
	Strategy *string
}

type fileConfig struct {
	Base     string
	PoolSize *int
	Strategy *string
}

func Load(cwd string, flags Overrides, lookupEnv func(string) (string, bool)) (Config, error) {
	file, err := discover(cwd)
	if err != nil {
		return Config{}, err
	}
	poolSize := 2
	if file.PoolSize != nil {
		poolSize = *file.PoolSize
	}
	size, err := strconv.Atoi(resolve(flags.PoolSize, lookupEnv, "PGCHECKOUT_POOL_SIZE", strconv.Itoa(poolSize)))
	if err != nil || size < 0 {
		return Config{}, NewError(ConfigInvalid, "pool size must be a non-negative integer", "")
	}

	strategy := "auto"
	if file.Strategy != nil {
		strategy = *file.Strategy
	}
	strategy = resolve(flags.Strategy, lookupEnv, "PGCHECKOUT_STRATEGY", strategy)
	switch strategy {
	case "auto", "clone", "file_copy", "wal_log", "default":
	default:
		return Config{}, NewError(ConfigInvalid, "strategy must be auto, clone, file_copy, wal_log, or default", "")
	}

	cfg := Config{
		Base:     resolve(flags.Base, lookupEnv, "PGCHECKOUT_BASE", file.Base),
		PoolSize: size,
		Strategy: strategy,
	}
	rawURL := resolve(flags.AdminURL, lookupEnv, "PGCHECKOUT_ADMIN_URL", "")
	if rawURL != "" {
		cfg.AdminURL, err = ParseAdminURL(rawURL)
	}
	return cfg, err
}

func ParseAdminURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Opaque != "" || u.OmitHost || u.Fragment != "" || !strings.HasPrefix(u.Path, "/") ||
		len(u.Path) < 2 || strings.IndexByte(u.Path, 0) >= 0 {
		return nil, NewError(ConfigInvalid, "admin URL must be a postgres:// or postgresql:// URL with a database path", "")
	}
	return u, nil
}

func (c Config) MaintenanceDB() string {
	return strings.TrimPrefix(c.AdminURL.Path, "/")
}

func (c Config) DatabaseURL(database string) string {
	u := *c.AdminURL
	u.Path = "/" + database
	u.RawPath = "/" + url.PathEscape(database)
	return u.String()
}

func resolve(flag *string, lookupEnv func(string) (string, bool), key, fallback string) string {
	if flag != nil {
		return *flag
	}
	if value, ok := lookupEnv(key); ok {
		return value
	}
	return fallback
}

func discover(cwd string) (fileConfig, error) {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return fileConfig{}, fmt.Errorf("resolve working directory: %w", err)
	}
	root := dir
	for candidate := dir; ; candidate = filepath.Dir(candidate) {
		if _, err := os.Lstat(filepath.Join(candidate, ".git")); err == nil {
			root = candidate
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return fileConfig{}, &Error{Code: ConfigInvalid, Message: "could not locate Git repository boundary", Cause: err}
		}
		if filepath.Dir(candidate) == candidate {
			break
		}
	}
	for ; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, ".pgcheckout.json")
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			return decodeFile(data)
		case !errors.Is(err, os.ErrNotExist):
			return fileConfig{}, &Error{Code: ConfigInvalid, Message: "could not read .pgcheckout.json", Cause: err}
		}
		if dir == root {
			return fileConfig{}, nil
		}
	}
}

func decodeFile(data []byte) (fileConfig, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return fileConfig{}, &Error{Code: ConfigInvalid, Message: ".pgcheckout.json must contain one JSON object", Cause: err}
	}
	var file fileConfig
	for key, value := range fields {
		var target any
		switch key {
		case "base":
			target = &file.Base
		case "pool_size":
			target = &file.PoolSize
		case "strategy":
			target = &file.Strategy
		default:
			return fileConfig{}, NewError(ConfigInvalid, ".pgcheckout.json contains an unknown key", "")
		}
		if err := json.Unmarshal(value, target); err != nil || bytes.Equal(value, []byte("null")) {
			return fileConfig{}, &Error{Code: ConfigInvalid, Message: "invalid value in .pgcheckout.json", Cause: err}
		}
	}
	return file, nil
}
