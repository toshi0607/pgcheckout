package mcpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toshi0607/pgcheckout/internal/config"
)

type Server struct {
	Version   string
	Overrides config.Overrides
	Refill    func(config.Config) (requested bool, warning string)

	cwd      string
	connect  func(context.Context, string) (*pgx.Conn, error)
	branches branchOperations
}

func (s Server) RunStdio(ctx context.Context, stdin io.ReadCloser, stdout io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	return s.run(ctx, &mcp.IOTransport{
		Reader: stdioReader{ReadCloser: stdin, cancel: cancel},
		Writer: stdioWriter{Writer: stdout},
	})
}

func (s Server) run(ctx context.Context, transport mcp.Transport) error {
	toolCtx, cancel := context.WithCancel(ctx)
	lifetime := toolsLifetime{ctx: toolCtx}

	sdk := mcp.NewServer(&mcp.Implementation{Name: "pgcheckout", Title: "pgcheckout", Version: s.Version}, &mcp.ServerOptions{
		Logger: slog.New(slog.DiscardHandler),
		Instructions: "Use these tools to check PostgreSQL status and manage databases for Git branches. " +
			"Ensure is idempotent and returns the existing database on repeated calls. " +
			"Returned URLs have their passwords removed, and env_file writes the full connection URL to disk. " +
			"Reset, drop, and gc delete data.",
	})
	s.registerTools(sdk, &lifetime)

	err := sdk.Run(ctx, transport)

	lifetime.mu.Lock()
	cancel()
	lifetime.mu.Unlock()
	lifetime.active.Wait()

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

type toolsLifetime struct {
	ctx    context.Context
	mu     sync.Mutex
	active sync.WaitGroup
}

func (l *toolsLifetime) begin() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() != nil {
		return false
	}
	l.active.Add(1)
	return true
}

type stdioReader struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r stdioReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		r.cancel()
	}
	return n, err
}

type stdioWriter struct{ io.Writer }

func (stdioWriter) Close() error { return nil }

func orderTools(order []string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if list, ok := result.(*mcp.ListToolsResult); ok && list != nil {
				// The SDK lists tools alphabetically. All eight tools fit on one page.
				slices.SortFunc(list.Tools, func(a, b *mcp.Tool) int {
					return slices.Index(order, a.Name) - slices.Index(order, b.Name)
				})
			}
			return result, err
		}
	}
}
