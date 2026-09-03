// Command temporaldb-cli is TemporalDB's interactive and scriptable client
// (ADR-001 D11): a REPL, a one-shot -e flag, and a -f script-file mode, all
// sending TQL over the wire via the client package (ADR-001 D9) - never
// opening the database file directly, which would break the single-writer
// invariant (ADR-001 D13).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/atvirokodosprendimai/temporaldbv1/client"
)

func main() {
	cmd := &cli.Command{
		Name:  "temporaldb-cli",
		Usage: "interactive and scriptable client for TemporalDB (TQL)",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "addr",
				Aliases: []string{"a"},
				Usage:   "TemporalDB server address",
				Value:   "http://localhost:7777",
				Sources: cli.EnvVars("TEMPORALDB_ADDR"),
			},
			&cli.StringFlag{
				Name:  "e",
				Usage: "execute one TQL command (or newline-separated batch) and exit",
			},
			&cli.StringFlag{
				Name:  "f",
				Usage: "execute the TQL commands in this file and exit",
			},
		},
		Action: run,
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "temporaldb-cli:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	c := client.New(cmd.String("addr"))

	switch {
	case cmd.String("e") != "":
		return execAndPrint(ctx, c, cmd.String("e"))
	case cmd.String("f") != "":
		data, err := os.ReadFile(cmd.String("f"))
		if err != nil {
			return fmt.Errorf("read %s: %w", cmd.String("f"), err)
		}
		return execAndPrint(ctx, c, string(data))
	default:
		return runREPL(ctx, c)
	}
}

func runREPL(ctx context.Context, c *client.Client) error {
	fmt.Println("TemporalDB CLI - type a TQL command, or 'exit' / Ctrl-D to quit.")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("tql> ")
		if !scanner.Scan() {
			fmt.Println()
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		switch line {
		case "":
			continue
		case "exit", "quit":
			return nil
		}
		if err := execAndPrint(ctx, c, line); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
}

// execAndPrint sends tqlText and prints each result as indented JSON, one
// per statement — the same wire shape the server and MCP tools use, so
// what you see here is exactly what a program consuming this API would
// get.
func execAndPrint(ctx context.Context, c *client.Client, tqlText string) error {
	qr, err := c.Query(ctx, tqlText)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	for _, res := range qr.Results {
		if err := enc.Encode(res); err != nil {
			return err
		}
	}
	if qr.Error != "" {
		return fmt.Errorf("%s", qr.Error)
	}
	return nil
}
