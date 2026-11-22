package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/toshi0607/pgcheckout/internal/branch"
	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/gitx"
	"github.com/toshi0607/pgcheckout/internal/naming"
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	options, format, err := parse(args)
	if err != nil {
		if problem, ok := errors.AsType[*config.Error](err); ok && problem.Code == config.Usage && problem.Hint == "" {
			problem.Hint = options.usageHint()
		}
		return writeError(stderr, format, err)
	}
	if options.help {
		if err := writeHelp(stdout, format, options.command, options.sub); err != nil {
			return writeError(stderr, format, err)
		}
		return 0
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
	if options.command == "hook" {
		result, err := options.executeHook(ctx, cwd)
		if err != nil {
			return writeError(stderr, format, err)
		}
		if err := writeResult(stdout, format, result); err != nil {
			return writeError(stderr, format, err)
		}
		return 0
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
	if ensured, ok := result.(branch.EnsureResult); ok {
		result, refill, err = options.prepareEnsureResult(cwd, cfg, ensured)
		if err != nil {
			return writeError(stderr, format, err)
		}
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

func (a arguments) executeHook(ctx context.Context, cwd string) (any, error) {
	if a.sub == "install" {
		return gitx.InstallHook(ctx, cwd, a.env, a.force)
	}
	return gitx.UninstallHook(ctx, cwd)
}

func (a arguments) prepareEnsureResult(cwd string, cfg config.Config, ensured branch.EnsureResult) (any, *exec.Cmd, error) {
	var env gitx.EnvResult
	if a.env.Path != nil {
		var err error
		env, err = writeEnsureEnv(cwd, a.env, ensured)
		if err != nil {
			return nil, nil, err
		}
	}
	var refill *exec.Cmd
	if ensured.RefillNeeded {
		var err error
		refill, err = prepareRefill(cfg)
		ensured.RefillRequested = refill != nil
		if err != nil {
			ensured.Warnings = append(ensured.Warnings, "could not prepare a detached pool refill")
		}
	}
	if a.env.Path == nil {
		return ensured, refill, nil
	}
	return struct {
		branch.EnsureResult
		gitx.EnvResult
	}{ensured, env}, refill, nil
}

func writeEnsureEnv(cwd string, opts gitx.EnvOptions, ensured branch.EnsureResult) (gitx.EnvResult, error) {
	result, err := opts.Write(cwd, ensured.URL)
	if err == nil {
		return result, nil
	}
	status := "already exists"
	if ensured.Created {
		status = "was created"
	}
	hint := fmt.Sprintf("The branch database %q %s.", ensured.Database, status)
	if problem, ok := errors.AsType[*config.Error](err); ok {
		if problem.Hint != "" {
			hint += " " + problem.Hint
		}
		return result, &config.Error{Code: problem.Code, Message: problem.Message, Hint: hint, Cause: err}
	}
	hint += fmt.Sprintf(" Check the path and permissions for %q.", *opts.Path)
	return result, &config.Error{Code: config.Internal, Message: "could not write env file", Hint: hint, Cause: err}
}

func (a arguments) validateConfig(cfg config.Config) error {
	if cfg.AdminURL == nil {
		return config.NewError(config.ConfigMissing, "admin URL is required", "Set PGCHECKOUT_ADMIN_URL or pass --admin-url.")
	}
	if a.command == "base" && (a.sub == "create" || a.sub == "adopt") {
		return naming.ValidateBase(a.pos[0])
	}
	if cfg.Base == "" {
		if a.command == "doctor" {
			return nil
		}
		return config.NewError(config.ConfigMissing, "base name is required", "Set PGCHECKOUT_BASE or pass --base.")
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
	return gitx.BranchName(ctx, cwd)
}

func prepareRefill(cfg config.Config) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve pgcheckout executable: %w", err)
	}
	executable, err = exec.LookPath(executable)
	if err != nil {
		return nil, fmt.Errorf("check pgcheckout executable: %w", err)
	}
	cmd := exec.Command(executable, "pool", "fill")
	if err := detach(cmd); err != nil {
		return nil, err
	}
	cmd.Env = append(cmd.Environ(),
		"PGCHECKOUT_ADMIN_URL="+cfg.AdminURL.String(),
		"PGCHECKOUT_BASE="+cfg.Base,
		"PGCHECKOUT_POOL_SIZE="+strconv.Itoa(cfg.PoolSize),
		"PGCHECKOUT_STRATEGY="+cmp.Or(cfg.Strategy, "auto"),
	)
	return cmd, nil
}
