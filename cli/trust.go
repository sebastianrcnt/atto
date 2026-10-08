package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/trust"
)

const trustUsage = `usage: atto trust [list [-json] | approve all | approve <kind> <name> | revoke all | revoke <kind> <name>]

  list                 project hooks, MCP servers and extensions, with their decisions
  approve all          approve the project's current executable content, by hash
  approve <kind> <name> approve one item (kind: hook, mcp, ext)
  revoke all           forget all this project's decisions, including removed items
  revoke <kind> <name> forget one item's decision; it needs approval again

Run in your own terminal, never from an agent's shell. Changes to approved
content need approval again. A running session picks decisions up with /reload.`

// RunTrust implements "atto trust". Approval decisions belong to the user, so
// even listing through this command is refused inside an agent's shell.
func RunTrust(args []string, out io.Writer) error {
	if config.InAgent() || os.Getenv("ATTO_SESSION_ID") != "" {
		return fmt.Errorf("atto: project trust is managed by the user, not from an agent's shell (ATTO_AGENT or ATTO_SESSION_ID is set). Ask the user to run atto trust in their terminal")
	}
	cmd := "list"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		_, err := fmt.Fprintln(out, trustUsage)
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if cmd == "revoke" && len(args) == 1 && args[0] == "all" {
		if err := trust.RevokeAll(cwd); err != nil {
			return err
		}
		fmt.Fprintln(out, "Revoked all project trust decisions.")
		return nil
	}
	items, err := trust.Discover(cwd)
	if err != nil {
		return err
	}
	switch cmd {
	case "list":
		fs := newFlags("trust list")
		asJSON := fs.Bool("json", false, "")
		if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
			return fmt.Errorf("%s", trustUsage)
		}
		if *asJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(items)
		}
		if len(items) == 0 {
			fmt.Fprintln(out, "This project brings no hooks, MCP servers or extensions that need approval.")
		}
		for _, in := range items {
			fmt.Fprintf(out, "%s  %s", in.Label(), in.Status)
			if in.Error != "" {
				fmt.Fprintf(out, ": %s", in.Error)
			}
			if in.Status == trust.Pending || in.Status == trust.Denied {
				fmt.Fprintf(out, " · %s", in.Command())
			}
			fmt.Fprintln(out)
		}
		return nil
	case "approve", "revoke":
		all := len(args) == 1 && args[0] == "all"
		if !all && len(args) != 2 {
			return fmt.Errorf("%s", trustUsage)
		}
		var selected []trust.Item
		for _, in := range items {
			if all || in.Kind == args[0] && in.Name == args[1] {
				selected = append(selected, in)
			}
		}
		if !all && len(selected) == 0 {
			return fmt.Errorf("no such project trust item %s (see atto trust)", strings.Join(args, " "))
		}
		if !all && len(selected) > 1 {
			return fmt.Errorf("ambiguous project trust item %s; resolve duplicate names first (see atto trust)", strings.Join(args, " "))
		}
		for _, in := range selected {
			if cmd == "approve" && all && (in.Status == trust.Failed || in.Status == trust.Disabled) {
				fmt.Fprintf(out, "Skipped %s %s (%s).\n", in.Kind, in.Name, in.Status)
				continue
			}
			var err error
			if cmd == "approve" {
				err = trust.Approve(in)
			} else {
				err = trust.Revoke(in)
			}
			if err != nil {
				return err
			}
			verb := "Approved"
			if cmd == "revoke" {
				verb = "Revoked"
			}
			fmt.Fprintf(out, "%s %s %s.\n", verb, in.Kind, in.Name)
		}
		return nil
	}
	return fmt.Errorf("%s", trustUsage)
}
