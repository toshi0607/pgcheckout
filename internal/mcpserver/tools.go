package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toshi0607/pgcheckout/internal/branch"
	"github.com/toshi0607/pgcheckout/internal/config"
)

type registration struct {
	server   Server
	sdk      *mcp.Server
	lifetime *toolsLifetime
	order    []string
}

func (s Server) registerTools(sdk *mcp.Server, lifetime *toolsLifetime) {
	r := registration{server: s, sdk: sdk, lifetime: lifetime}
	readOnly := mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)}
	ensure := mcp.ToolAnnotations{DestructiveHint: new(false), IdempotentHint: true, OpenWorldHint: new(false)}
	destructive := mcp.ToolAnnotations{DestructiveHint: new(true), OpenWorldHint: new(false)}

	addTool(&r, "doctor", "Check PostgreSQL readiness",
		"Check the PostgreSQL server, role, and base before managing branch databases.", readOnly, toolCall.doctor)
	addTool(&r, "list", "List databases",
		"List the base, branch databases, pooled and temporary copies, and unmanaged databases with reserved names.", readOnly, toolCall.list)
	addTool(&r, "pool_status", "Check pool status",
		"Inspect pooled copies and readiness before requesting a branch database.", readOnly, toolCall.poolStatus)
	addTool(&r, "dsn", "Get branch connection URL",
		"Look up an existing branch database and return its password-redacted connection URL.", readOnly, toolCall.dsn)
	addTool(&r, "ensure", "Ensure branch database",
		"Get or create a branch database without deleting existing data. Use env_file to save the full connection URL.", ensure, toolCall.ensure)
	addTool(&r, "reset", "Reset branch database",
		"Delete the named branch database and recreate it from the current base. Use this to discard changes or refresh a stale branch.", destructive, toolCall.reset)
	addTool(&r, "drop", "Drop branch database",
		"Delete the named branch database and all of its data when the branch database is no longer needed.", destructive, toolCall.drop)
	addTool(&r, "gc", "Collect expired and obsolete databases",
		"Delete expired branch databases and obsolete pooled or temporary copies. Set dry_run to preview deletions.", destructive, toolCall.gc)

	sdk.AddReceivingMiddleware(orderTools(r.order))
}

type toolCall struct {
	cwd    string
	svc    branch.Service
	refill func(config.Config) (bool, string)
}

func addTool[In, Out any](r *registration, name, title, description string, annotations mcp.ToolAnnotations,
	run func(toolCall, context.Context, In) (Out, error),
) {
	input := inferSchema[In]()
	for _, prop := range input.Properties {
		if len(prop.Types) == 2 && prop.Types[0] == "null" {
			prop.Type, prop.Types = prop.Types[1], nil
		}
	}
	// Advertise required fields and check their presence separately so callers
	// get pgcheckout's usage result for omissions and JSON-RPC errors for bad types.
	wireInput := *input
	wireInput.Required = nil
	inputValidator := resolveSchema(&wireInput)
	output := inferSchema[Out]()
	outputValidator := resolveSchema(output)
	server, lifetime := r.server, r.lifetime
	r.order = append(r.order, name)
	r.sdk.AddTool(&mcp.Tool{
		Name: name, Title: title, Description: description, Annotations: &annotations,
		InputSchema: input, OutputSchema: output,
	}, func(requestCtx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !lifetime.begin() {
			return toolError(config.Config{}, lifetime.ctx.Err())
		}
		defer lifetime.active.Done()

		ctx, cancel := context.WithCancel(requestCtx)
		stop := context.AfterFunc(lifetime.ctx, cancel)
		defer cancel()
		defer stop()

		args := req.Params.Arguments
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		var fields map[string]any
		var in In
		if err := json.Unmarshal(args, &fields); err != nil || fields == nil ||
			inputValidator.Validate(fields) != nil || json.Unmarshal(args, &in) != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid tool arguments"}
		}
		if fields["branch"] == "" || (fields["branch"] == nil && slices.Contains(input.Required, "branch")) {
			return toolError(config.Config{}, config.NewError(config.Usage, "branch name is required", "Pass a branch name explicitly."))
		}

		cwd := server.cwd
		if cwd == "" {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				return toolError(config.Config{}, err)
			}
		}
		cfg, err := config.Load(cwd, server.Overrides, os.LookupEnv)
		if err != nil {
			return toolError(cfg, err)
		}
		if err := branch.ValidateConfig(cfg, name != "doctor"); err != nil {
			return toolError(cfg, err)
		}
		call := toolCall{cwd: cwd, svc: branch.Service{Config: cfg, Connect: server.connect}, refill: server.Refill}
		out, err := run(call, ctx, in)
		if err != nil {
			return toolError(cfg, err)
		}
		data, err := json.Marshal(out)
		if err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "could not encode tool output"}
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil || outputValidator.Validate(value) != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "tool output did not match its schema"}
		}
		return &mcp.CallToolResult{
			StructuredContent: json.RawMessage(data),
			Content:           []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil
	})
}

func inferSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Errorf("infer MCP schema: %w", err))
	}
	return schema
}

func resolveSchema(schema *jsonschema.Schema) *jsonschema.Resolved {
	resolved, err := schema.Resolve(nil)
	if err != nil {
		panic(fmt.Errorf("resolve MCP schema: %w", err))
	}
	return resolved
}

func toolError(cfg config.Config, err error) (*mcp.CallToolResult, error) {
	payload := config.NewErrorResponse(err)
	payload.Error.Message = redactDiagnostic(cfg, payload.Error.Message)
	payload.Error.Hint = redactDiagnostic(cfg, payload.Error.Hint)
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "could not encode tool error"}
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil
}
