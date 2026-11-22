package cli

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/toshi0607/pgtwig/internal/branch"
	"github.com/toshi0607/pgtwig/internal/config"
	"github.com/toshi0607/pgtwig/internal/naming"
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	options, format, err := parse(args)
	if err != nil {
		return writeError(stderr, format, err)
	}
	if options.command == "version" {
		if err := writeResult(stdout, format, map[string]string{"version": version}); err != nil {
			return writeError(stderr, format, err)
		}
		return 0
	}
	cwd, err := os.Getwd()
	if err != nil {
		return writeError(stderr, format, fmt.Errorf("get working directory: %w", err))
	}
	cfg, err := config.Load(cwd, options.overrides, os.LookupEnv)
	if err != nil {
		return writeError(stderr, format, err)
	}
	if err := options.validateConfig(cfg); err != nil {
		return writeError(stderr, format, err)
	}
	result, err := options.execute(ctx, cwd, branch.Service{Config: cfg})
	if err != nil {
		return writeError(stderr, format, err)
	}

	var refill *exec.Cmd
	if ensured, ok := result.(branch.EnsureResult); ok && ensured.RefillNeeded {
		refill, err = prepareRefill(cfg)
		ensured.RefillStarted = refill != nil
		if err != nil {
			ensured.Warnings = append(ensured.Warnings, "could not prepare a detached pool refill")
		}
		result = ensured
	}
	if err := writeResult(stdout, format, result); err != nil {
		return writeError(stderr, format, err)
	}
	if refill != nil {
		if err := refill.Start(); err != nil {
			writeRefillWarning(stderr, format)
		} else if err := refill.Process.Release(); err != nil {
			writeReleaseWarning(stderr, format)
		}
	}
	return 0
}

func (a arguments) validateConfig(cfg config.Config) error {
	if cfg.AdminURL == nil {
		return config.NewError(config.ConfigMissing, "admin URL is required", "Set PGTWIG_ADMIN_URL or pass --admin-url.")
	}
	if a.command == "base" && (a.sub == "create" || a.sub == "adopt") {
		return naming.ValidateBase(a.pos[0])
	}
	if cfg.Base == "" {
		if a.command == "doctor" {
			return nil
		}
		return config.NewError(config.ConfigMissing, "base name is required", "Set PGTWIG_BASE or pass --base.")
	}
	return naming.ValidateBase(cfg.Base)
}

func (a arguments) execute(ctx context.Context, cwd string, svc branch.Service) (any, error) {
	switch a.command {
	case "ensure", "reset", "drop", "dsn":
		name, err := a.branchName(ctx, cwd)
		if err != nil {
			return nil, err
		}
		opts := branch.EnsureOptions{TTL: a.ttl, NoRefill: a.noRefill}
		switch a.command {
		case "ensure":
			return svc.Ensure(ctx, name, opts)
		case "reset":
			return svc.Reset(ctx, name, a.force, opts)
		case "drop":
			return svc.Drop(ctx, name, a.force)
		case "dsn":
			return svc.DSN(ctx, name)
		}
	case "list":
		return svc.List(ctx)
	case "gc":
		return svc.GC(ctx, a.dryRun)
	case "doctor":
		return svc.Doctor(ctx)
	case "base":
		switch a.sub {
		case "create":
			return svc.BaseCreate(ctx, a.pos[0], a.from, a.terminate)
		case "adopt":
			return svc.BaseAdopt(ctx, a.pos[0], a.terminate)
		case "open":
			return svc.BaseOpen(ctx)
		case "close":
			return svc.BaseClose(ctx, a.terminate)
		}
	case "pool":
		switch a.sub {
		case "fill":
			size := svc.Config.PoolSize
			if a.size != nil {
				size = *a.size
			}
			return svc.PoolFill(ctx, size)
		case "status":
			return svc.PoolStatus(ctx)
		case "drain":
			return svc.PoolDrain(ctx)
		}
	}
	return nil, usage("unknown command")
}

func (a arguments) branchName(ctx context.Context, cwd string) (string, error) {
	if len(a.pos) == 1 {
		return a.pos[0], nil
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = cwd
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok && bytes.Contains(exit.Stderr, []byte("not a git repository")) {
			return "", &config.Error{Code: config.NotGitRepo, Message: "current directory is not in a Git repository",
				Hint: "Pass a branch name explicitly.", Cause: err}
		}
		return "", fmt.Errorf("resolve Git branch: %w", err)
	}
	name := strings.TrimRight(string(out), "\r\n")
	if name == "HEAD" {
		return "", config.NewError(config.DetachedHead, "Git HEAD is detached", "Pass a branch name explicitly.")
	}
	return name, nil
}

func prepareRefill(cfg config.Config) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve pgtwig executable: %w", err)
	}
	executable, err = exec.LookPath(executable)
	if err != nil {
		return nil, fmt.Errorf("check pgtwig executable: %w", err)
	}
	cmd := exec.Command(executable, "pool", "fill")
	if err := detach(cmd); err != nil {
		return nil, err
	}
	cmd.Env = append(cmd.Environ(),
		"PGTWIG_ADMIN_URL="+cfg.AdminURL.String(),
		"PGTWIG_BASE="+cfg.Base,
		"PGTWIG_POOL_SIZE="+strconv.Itoa(cfg.PoolSize),
		"PGTWIG_STRATEGY="+cmp.Or(cfg.Strategy, "auto"),
	)
	return cmd, nil
}
