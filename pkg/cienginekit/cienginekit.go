package cienginekit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/alexandremahdhaoui/forge/pkg/enginecli"
	"github.com/alexandremahdhaoui/forge/pkg/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Handler[In any, Out any] func(context.Context, In) (Out, error)

type Tool struct {
	Name        string
	Description string
	register    func(*mcpserver.Server)
	invoke      func(context.Context, []byte) (any, error)
}

func NewTool[In any, Out any](name, description string, h Handler[In, Out]) Tool {
	t := Tool{Name: name, Description: description}

	t.register = func(s *mcpserver.Server) {
		mcpserver.RegisterTool(s, &mcp.Tool{Name: name, Description: description},
			func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
				out, err := h(ctx, in)
				if err != nil {
					return nil, nil, err
				}

				return nil, out, nil
			})
	}

	t.invoke = func(ctx context.Context, raw []byte) (any, error) {
		var in In

		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, fmt.Errorf("decoding input for %s: %w", name, err)
			}
		}

		return h(ctx, in)
	}

	return t
}

type Engine struct {
	Name    string
	Version string
	Tools   []Tool
}

func (e Engine) Run() {
	enginecli.Bootstrap(enginecli.Config{
		Name:    e.Name,
		Version: e.Version,
		RunMCP:  e.runMCP,
		RunCLI:  func() error { return e.RunCLI(os.Args[1:], os.Stdin, os.Stdout) },
		FailureHandler: func(err error) {
			fmt.Fprintf(os.Stderr, "%s: %v\n", e.Name, err)
		},
	})
}

func (e Engine) runMCP() error {
	server := mcpserver.New(e.Name, e.Version)

	for _, t := range e.Tools {
		t.register(server)
	}

	return server.RunDefault()
}

func (e Engine) RunCLI(args []string, stdin *os.File, stdout *os.File) error {
	if len(args) == 0 {
		return fmt.Errorf("%s: usage: %s <tool>, with the input as JSON on stdin. tools: %s",
			e.Name, e.Name, e.toolNames())
	}

	for _, t := range e.Tools {
		if t.Name != args[0] {
			continue
		}

		raw, err := readAll(stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}

		out, err := t.invoke(context.Background(), raw)
		if err != nil {
			return err
		}

		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")

		if err := enc.Encode(out); err != nil {
			return fmt.Errorf("writing result: %w", err)
		}

		return nil
	}

	return fmt.Errorf("%s: unknown tool %q, want one of %s", e.Name, args[0], e.toolNames())
}

func (e Engine) toolNames() string {
	names := make([]string, 0, len(e.Tools))
	for _, t := range e.Tools {
		names = append(names, t.Name)
	}

	return fmt.Sprintf("%v", names)
}

func readAll(f *os.File) ([]byte, error) {
	if f == nil {
		return nil, nil
	}

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspecting stdin: %w", err)
	}

	if info.Mode()&os.ModeCharDevice != 0 {
		return nil, nil
	}

	return io.ReadAll(f)
}
