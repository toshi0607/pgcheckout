package cli

import (
	"errors"
	"flag"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/toshi0607/pgcheckout/internal/branch"
	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/gitx"
)

type arguments struct {
	help      bool
	command   string
	sub       string
	pos       []string
	overrides config.Overrides
	ttl       *time.Duration
	size      *int
	from      string
	force     bool
	terminate bool
	noRefill  bool
	dryRun    bool
	env       gitx.EnvOptions
}

func parse(args []string) (options arguments, format string, err error) {
	global := flag.NewFlagSet("pgcheckout", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	adminURL := global.String("admin-url", "", "")
	base := global.String("base", "", "")
	poolSize := global.String("pool-size", "", "")
	strategy := global.String("strategy", "", "")
	output := global.String("format", "json", "")
	globalArgs, rest := globalFlags(global, args)
	if err := global.Parse(globalArgs); err != nil {
		return options, *output, usage("invalid global flags")
	}
	format = *output
	if format != "json" && format != "text" && (len(rest) == 0 || rest[0] != "mcp") {
		return options, format, usage("--format must be json or text")
	}
	if len(rest) > 0 && rest[0] == "mcp" {
		format = "json"
	}
	global.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "admin-url":
			options.overrides.AdminURL = adminURL
		case "base":
			options.overrides.Base = base
		case "pool-size":
			options.overrides.PoolSize = poolSize
		case "strategy":
			options.overrides.Strategy = strategy
		}
	})
	if len(rest) == 0 {
		return options, format, usage("a command is required")
	}
	options.command, rest = rest[0], rest[1:]
	if options.command == "help" || isHelpFlag(options.command) {
		if options.command == "help" && len(rest) == 1 && isHelpFlag(rest[0]) {
			options.help = true
			return options, format, nil
		}
		if len(rest) != 0 {
			return options, format, usage("unexpected positional arguments")
		}
		options.command, options.help = "", true
		return options, format, nil
	}
	switch options.command {
	case "base", "pool", "hook":
		if len(rest) == 0 {
			return options, format, usage("a subcommand is required")
		}
		if isHelpFlag(rest[0]) {
			options.help = true
			return options, format, nil
		}
		options.sub, rest = rest[0], rest[1:]
	}

	local := flag.NewFlagSet(options.command, flag.ContinueOnError)
	local.SetOutput(io.Discard)
	err = options.bind(local)
	if err != nil {
		return options, format, err
	}
	ordered, err := localFlags(local, rest)
	if errors.Is(err, flag.ErrHelp) {
		options.help = true
		return options, format, nil
	}
	if err != nil {
		return options, format, err
	}
	if err := local.Parse(ordered); err != nil {
		return options, format, usage("invalid command flags")
	}
	options.pos = local.Args()
	if err := options.validate(); err != nil {
		return options, format, err
	}
	return options, format, nil
}

func (a *arguments) bind(local *flag.FlagSet) error {
	switch a.command {
	case "ensure", "reset":
		a.bindEnv(local)
		local.Func("ttl", "", func(value string) error {
			ttl, err := time.ParseDuration(value)
			if err != nil {
				return err
			}
			a.ttl = &ttl
			return nil
		})
		local.BoolVar(&a.noRefill, "no-refill", false, "")
		if a.command == "reset" {
			local.BoolVar(&a.force, "force", false, "")
		}
	case "drop":
		local.BoolVar(&a.force, "force", false, "")
	case "gc":
		local.BoolVar(&a.dryRun, "dry-run", false, "")
	case "hook":
		switch a.sub {
		case "install":
			a.bindEnv(local)
			local.BoolVar(&a.force, "force", false, "")
		case "uninstall":
		default:
			return usage("unknown hook subcommand")
		}
	case "pool":
		switch a.sub {
		case "fill":
			local.Func("size", "", func(value string) error {
				size, err := strconv.Atoi(value)
				if err != nil {
					return err
				}
				a.size = &size
				return nil
			})
		case "status", "drain":
		default:
			return usage("unknown pool subcommand")
		}
	case "base":
		switch a.sub {
		case "create":
			local.StringVar(&a.from, "from", "", "")
			fallthrough
		case "adopt", "close":
			local.BoolVar(&a.terminate, "terminate", false, "")
		case "open":
		default:
			return usage("unknown base subcommand")
		}
	case "list", "dsn", "doctor", "version", "mcp":
	default:
		return usage("unknown command")
	}
	return nil
}

func (a *arguments) bindEnv(local *flag.FlagSet) {
	local.Func("env-file", "", func(value string) error {
		a.env.Path = new(value)
		return nil
	})
	local.Func("env-key", "", func(value string) error {
		a.env.Key = new(value)
		return nil
	})
}

func (a *arguments) validate() error {
	switch {
	case a.command == "base" && (a.sub == "create" || a.sub == "adopt"):
		if len(a.pos) != 1 {
			return usage("base create and base adopt require exactly one database name")
		}
		if a.sub == "create" && a.from == "" {
			return usage("base create requires --from")
		}
	case a.command == "ensure" || a.command == "reset" || a.command == "drop" || a.command == "dsn":
		if len(a.pos) > 1 {
			return usage("expected at most one branch name")
		}
	default:
		if len(a.pos) > 0 {
			return usage("unexpected positional arguments")
		}
	}
	if a.size != nil && *a.size < 0 {
		return usage("--size must be non-negative")
	}
	if err := branch.ValidateTTL(a.ttl); err != nil {
		return err
	}
	return a.env.Validate()
}

func flagName(arg string) (name string, attached bool) {
	if arg == "-" || !strings.HasPrefix(arg, "-") {
		return "", false
	}
	name, _, attached = strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
	return name, attached
}

func globalFlags(global *flag.FlagSet, args []string) (flags, rest []string) {
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			return flags, append(append(rest, arg), args...)
		}
		name, attached := flagName(arg)
		switch {
		case global.Lookup(name) != nil:
			flags = append(flags, arg)
			if !attached && len(args) > 0 {
				flags, args = append(flags, args[0]), args[1:]
			}
		case name == "from" || name == "ttl" || name == "size" || name == "env-file" || name == "env-key":
			rest = append(rest, arg)
			if !attached && len(args) > 0 {
				rest, args = append(rest, args[0]), args[1:]
			}
		default:
			rest = append(rest, arg)
		}
	}
	return flags, rest
}

func localFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, pos []string
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			pos = append(pos, args...)
			break
		}
		if isHelpFlag(arg) {
			return nil, flag.ErrHelp
		}
		name, attached := flagName(arg)
		if name == "" {
			pos = append(pos, arg)
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			return nil, usage("unknown command flag")
		}
		flags = append(flags, arg)
		boolFlag, isBool := f.Value.(interface{ IsBoolFlag() bool })
		if !attached && !(isBool && boolFlag.IsBoolFlag()) {
			if len(args) == 0 {
				return nil, usage("command flag requires a value")
			}
			flags, args = append(flags, args[0]), args[1:]
		}
	}
	return append(append(flags, "--"), pos...), nil
}

func isHelpFlag(arg string) bool { return arg == "--help" || arg == "-h" }

func usage(message string) *config.Error {
	return config.NewError(config.Usage, message, "")
}

func (a arguments) usageHint() string {
	switch a.command {
	case "":
		return "Run pgcheckout help."
	case "ensure":
		return "pgcheckout ensure [branch] [--ttl <duration>] [--no-refill] [--env-file <path> [--env-key <NAME>]]"
	case "reset":
		return "pgcheckout reset [branch] [--force] [--ttl <duration>] [--no-refill] [--env-file <path> [--env-key <NAME>]]"
	case "hook":
		switch a.sub {
		case "install":
			return "pgcheckout hook install [--env-file <path> [--env-key <NAME>]] [--force]"
		case "uninstall":
			return "pgcheckout hook uninstall"
		default:
			return "pgcheckout hook install | uninstall"
		}
	}
	return ""
}
