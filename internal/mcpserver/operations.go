package mcpserver

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/toshi0607/pgcheckout/internal/branch"
	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/gitx"
)

type emptyInput struct{}

type currentBranchInput struct {
	Branch string `json:"branch,omitempty" jsonschema:"Git branch name. Defaults to the current branch of the server's working directory."`
}

type ensureSettings struct {
	TTL     *string `json:"ttl,omitempty" jsonschema:"Positive Go duration for a newly assigned database, such as 72h."`
	EnvFile *string `json:"env_file,omitempty" jsonschema:"Absolute path or path relative to the server's working directory for writing the full connection URL as DATABASE_URL."`
	EnvKey  *string `json:"env_key,omitempty" jsonschema:"Variable name to use instead of DATABASE_URL. Requires env_file."`
}

type ensureInput struct {
	currentBranchInput
	ensureSettings
}

type dropInput struct {
	Branch string `json:"branch" jsonschema:"Git branch name of the database to delete."`
	Force  bool   `json:"force,omitzero" jsonschema:"Disconnect sessions before deleting the branch database."`
}

type resetInput struct {
	dropInput
	ensureSettings
}

type gcInput struct {
	DryRun bool `json:"dry_run,omitzero" jsonschema:"Preview eligible databases without deleting them."`
}

type dsnOutput struct {
	branch.DSNResult
	PasswordRedacted bool `json:"password_redacted"`
}

type ensureOutput struct {
	branch.EnsureResult
	PasswordRedacted bool   `json:"password_redacted"`
	EnvFile          string `json:"env_file,omitempty"`
	EnvFileChanged   *bool  `json:"env_file_changed,omitempty"`
}

func (c toolCall) doctor(ctx context.Context, _ emptyInput) (branch.DoctorResult, error) {
	result, err := c.svc.Doctor(ctx)
	result.Unmanaged = nonNil(result.Unmanaged)
	result.Problems = nonNil(result.Problems)
	result.BaseReason = redactDiagnostic(c.svc.Config, result.BaseReason)
	for i := range result.Problems {
		problem := &result.Problems[i]
		problem.Message = redactDiagnostic(c.svc.Config, problem.Message)
		problem.Hint = redactDiagnostic(c.svc.Config, problem.Hint)
	}
	return result, err
}

func (c toolCall) list(ctx context.Context, _ emptyInput) (branch.ListResult, error) {
	result, err := c.svc.List(ctx)
	result.Branches = nonNil(result.Branches)
	result.Pool = nonNil(result.Pool)
	result.Tmp = nonNil(result.Tmp)
	result.Unmanaged = nonNil(result.Unmanaged)
	return result, err
}

func (c toolCall) poolStatus(ctx context.Context, _ emptyInput) (branch.PoolStatus, error) {
	result, err := c.svc.PoolStatus(ctx)
	result.Databases = nonNil(result.Databases)
	return result, err
}

func (c toolCall) dsn(ctx context.Context, in currentBranchInput) (dsnOutput, error) {
	name, err := c.branchName(ctx, in.Branch)
	if err != nil {
		return dsnOutput{}, err
	}
	result, err := c.svc.DSN(ctx, name)
	if err != nil {
		return dsnOutput{}, err
	}
	redactedURL, redacted, err := redactURL(result.URL)
	result.URL = redactedURL
	return dsnOutput{DSNResult: result, PasswordRedacted: redacted}, err
}

func (c toolCall) ensure(ctx context.Context, in ensureInput) (ensureOutput, error) {
	opts, env, err := in.ensureSettings.options()
	if err != nil {
		return ensureOutput{}, err
	}
	name, err := c.branchName(ctx, in.Branch)
	if err != nil {
		return ensureOutput{}, err
	}
	result, err := c.svc.Ensure(ctx, name, opts)
	if err != nil {
		return ensureOutput{}, err
	}
	return c.finishEnsure(result, env)
}

func (c toolCall) reset(ctx context.Context, in resetInput) (ensureOutput, error) {
	opts, env, err := in.ensureSettings.options()
	if err != nil {
		return ensureOutput{}, err
	}
	result, err := c.svc.Reset(ctx, in.Branch, in.Force, opts)
	if err != nil {
		return ensureOutput{}, err
	}
	return c.finishEnsure(result, env)
}

func (c toolCall) finishEnsure(result branch.EnsureResult, env gitx.EnvOptions) (ensureOutput, error) {
	var out ensureOutput
	if env.Path != nil {
		written, err := env.WriteBranch(c.cwd, result.URL, result.Database, result.Created)
		if err != nil {
			return out, err
		}
		out.EnvFile, out.EnvFileChanged = written.EnvFile, new(written.EnvFileChanged)
	}
	redactedURL, redacted, err := redactURL(result.URL)
	if err != nil {
		return out, err
	}
	result.URL, out.PasswordRedacted = redactedURL, redacted
	if result.RefillNeeded && c.refill != nil {
		var warning string
		result.RefillRequested, warning = c.refill(c.svc.Config)
		if warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
	}
	c.redactWarnings(result.Warnings)
	out.EnsureResult = result
	return out, nil
}

func (c toolCall) drop(ctx context.Context, in dropInput) (branch.DropResult, error) {
	result, err := c.svc.Drop(ctx, in.Branch, in.Force)
	c.redactWarnings(result.Warnings)
	return result, err
}

func (c toolCall) gc(ctx context.Context, in gcInput) (branch.GCResult, error) {
	result, err := c.svc.GC(ctx, in.DryRun)
	result.Dropped = nonNil(result.Dropped)
	result.Skipped = nonNil(result.Skipped)
	c.redactWarnings(result.Warnings)
	return result, err
}

func (c toolCall) branchName(ctx context.Context, name string) (string, error) {
	if name != "" {
		return name, nil
	}
	return gitx.BranchName(ctx, c.cwd)
}

func (in ensureSettings) options() (opts branch.EnsureOptions, env gitx.EnvOptions, err error) {
	if in.TTL != nil {
		ttl, err := time.ParseDuration(*in.TTL)
		if err != nil {
			return opts, env, config.NewError(config.Usage, "invalid command flags", "")
		}
		opts.TTL = &ttl
	}
	if err := branch.ValidateTTL(opts.TTL); err != nil {
		return opts, env, err
	}
	env = gitx.EnvOptions{Path: in.EnvFile, Key: in.EnvKey}
	return opts, env, env.Validate()
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func redactURL(raw string) (string, bool, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false, config.NewError(config.Internal, "could not redact branch URL", "")
	}
	_, redacted := u.User.Password()
	if redacted {
		u.User = url.User(u.User.Username())
	}

	var query []string
	for part := range strings.SplitSeq(u.RawQuery, "&") {
		key, _, _ := strings.Cut(part, "=")
		if decoded, err := url.QueryUnescape(key); err == nil && passwordParameter(decoded) {
			redacted = true
			continue
		}
		query = append(query, part)
	}
	if !redacted {
		return raw, false, nil
	}
	u.RawQuery = strings.Join(query, "&")
	return u.String(), true, nil
}

func passwordParameter(key string) bool {
	return strings.EqualFold(key, "password") || strings.EqualFold(key, "sslpassword")
}

func (c toolCall) redactWarnings(warnings []string) {
	for i, warning := range warnings {
		warnings[i] = redactDiagnostic(c.svc.Config, warning)
	}
}

func redactDiagnostic(cfg config.Config, message string) string {
	message = config.RedactConnectionDetails(message)
	if cfg.AdminURL == nil {
		return message
	}
	if password, ok := cfg.AdminURL.User.Password(); ok && password != "" {
		message = strings.ReplaceAll(message, password, "<redacted>")
	}
	for key, values := range cfg.AdminURL.Query() {
		if !passwordParameter(key) {
			continue
		}
		for _, password := range values {
			if password != "" {
				message = strings.ReplaceAll(message, password, "<redacted>")
			}
		}
	}
	return message
}
