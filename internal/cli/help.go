package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

type helpFlag struct {
	Name    string `json:"name"`
	Value   string `json:"value,omitempty"`
	Summary string `json:"summary"`
}

type helpCommand struct {
	Name    string     `json:"name"`
	Summary string     `json:"summary"`
	Flags   []helpFlag `json:"flags"`
	args    string
}

type helpEnv struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

type helpResult struct {
	Usage       string            `json:"usage"`
	Commands    []helpCommand     `json:"commands"`
	GlobalFlags []helpFlag        `json:"global_flags"`
	Env         []helpEnv         `json:"env"`
	ExitCodes   map[string]string `json:"exit_codes"`
}

var envFileFlags = []helpFlag{
	{"--env-file", "path", "Write the branch connection URL to a managed env file."},
	{"--env-key", "name", "Use this variable name in the env file (default DATABASE_URL)."},
}

var ensureFlags = slices.Concat([]helpFlag{
	{"--ttl", "duration", "Set a positive lifetime for a newly assigned database."},
	{"--no-refill", "", "Skip requesting a background pool refill."},
}, envFileFlags)

var forceFlag = helpFlag{"--force", "", "Disconnect sessions before dropping the branch database."}
var terminateFlag = helpFlag{"--terminate", "", "Disconnect database sessions."}

var help = helpResult{
	Usage: "pgcheckout [global flags] <command> ...",
	Commands: []helpCommand{
		{"ensure", "Get or create a database for a branch.", ensureFlags, "[branch]"},
		{"dsn", "Get the connection URL for a branch database.", nil, "[branch]"},
		{"list", "List the base, branches, pool and unmanaged databases.", nil, ""},
		{"drop", "Drop a branch database.", []helpFlag{forceFlag}, "[branch]"},
		{"reset", "Recreate a branch database from the current base.", slices.Concat(ensureFlags, []helpFlag{forceFlag}), "[branch]"},
		{"pool", "Manage pooled copies.", nil, "<fill|status|drain>"},
		{"pool fill", "Fill the pool to its target size.", []helpFlag{{"--size", "integer", "Override the target pool size."}}, ""},
		{"pool status", "Show the pool and its current size.", nil, ""},
		{"pool drain", "Drop pool and temporary databases.", nil, ""},
		{"gc", "Remove expired branch databases and obsolete copies.", []helpFlag{{"--dry-run", "", "Show what would be dropped."}}, ""},
		{"doctor", "Report server, role and base status.", nil, ""},
		{"base", "Manage the base database.", nil, "<create|adopt|open|close>"},
		{"base create", "Copy a source database to make a base.", []helpFlag{
			{"--from", "database", "Copy this source database (required)."}, terminateFlag,
		}, "<name>"},
		{"base adopt", "Mark an existing database as the base and close it.", []helpFlag{terminateFlag}, "<name>"},
		{"base open", "Open the base for changes.", nil, ""},
		{"base close", "Close the base and advance its generation.", []helpFlag{terminateFlag}, ""},
		{"hook", "Manage the Git post-checkout hook.", nil, "<install|uninstall>"},
		{"hook install", "Install the Git post-checkout hook.", slices.Concat(envFileFlags, []helpFlag{
			{"--force", "", "Back up and replace an existing unmanaged hook."},
		}), ""},
		{"hook uninstall", "Remove the managed Git post-checkout hook.", nil, ""},
		{"version", "Print the pgcheckout version.", nil, ""},
		{"help", "Show command usage.", nil, ""},
	},
	GlobalFlags: []helpFlag{
		{"--admin-url", "url", "Set the maintenance database connection URL."},
		{"--base", "name", "Set the base database name."},
		{"--pool-size", "integer", "Set the pool target size (default 2)."},
		{"--strategy", "name", "Choose auto, clone, file_copy, wal_log or default."},
		{"--format", "json|text", "Set the output format (default json)."},
		{"--help", "", "Show usage (also -h)."},
	},
	Env: []helpEnv{
		{"PGCHECKOUT_ADMIN_URL", "Maintenance database connection URL."},
		{"PGCHECKOUT_BASE", "Base database name."},
		{"PGCHECKOUT_POOL_SIZE", "Target number of pooled copies."},
		{"PGCHECKOUT_STRATEGY", "Database copy strategy."},
	},
	ExitCodes: map[string]string{"0": "success", "1": "runtime error", "2": "usage error"},
}

func writeHelp(out io.Writer, format, command, sub string) error {
	result := help
	result.Commands = make([]helpCommand, 0, len(help.Commands))
	if sub != "" {
		command += " " + sub
	}
	for _, entry := range help.Commands {
		if command == "" || entry.Name == command {
			if entry.Flags == nil {
				entry.Flags = []helpFlag{}
			}
			result.Commands = append(result.Commands, entry)
		}
	}
	if len(result.Commands) == 0 {
		return usage("unknown help command")
	}
	if format == "json" {
		return writeResult(out, format, result)
	}

	var text strings.Builder
	if command == "" {
		fmt.Fprintln(&text, "Usage:", result.Usage)
		fmt.Fprintln(&text, "\nCommands:")
	} else {
		entry := result.Commands[0]
		usage := "pgcheckout [global flags] " + entry.Name
		if entry.args != "" {
			usage += " " + entry.args
		}
		if len(entry.Flags) > 0 {
			usage += " [flags]"
		}
		fmt.Fprintln(&text, "Usage:", usage)
		fmt.Fprintln(&text, "\n"+entry.Summary)
	}
	if command == "" {
		for _, entry := range result.Commands {
			fmt.Fprintf(&text, "  %-18s %s\n", entry.Name, entry.Summary)
		}
	} else if flags := result.Commands[0].Flags; len(flags) > 0 {
		fmt.Fprintln(&text, "\nCommand flags:")
		writeHelpFlags(&text, flags)
	}
	fmt.Fprintln(&text, "\nGlobal flags:")
	writeHelpFlags(&text, result.GlobalFlags)
	fmt.Fprintln(&text, "\nEnvironment:")
	for _, env := range result.Env {
		fmt.Fprintf(&text, "  %-24s %s\n", env.Name, env.Summary)
	}
	fmt.Fprintln(&text, "\nExit codes:")
	for _, code := range []string{"0", "1", "2"} {
		fmt.Fprintf(&text, "  %s  %s\n", code, result.ExitCodes[code])
	}
	_, err := io.WriteString(out, text.String())
	return err
}

func writeHelpFlags(text *strings.Builder, flags []helpFlag) {
	for _, flag := range flags {
		name := flag.Name
		if flag.Value != "" {
			name += " <" + flag.Value + ">"
		}
		fmt.Fprintf(text, "  %-24s %s\n", name, flag.Summary)
	}
}
