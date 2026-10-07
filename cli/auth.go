package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/textfmt"
)

// RunAuth implements "atto auth set <provider>": it reads an API key
// (without echo on a terminal) and stores it in ~/.atto/auth.json.
func RunAuth(args []string, out io.Writer) error {
	if len(args) != 2 || args[0] != "set" {
		return fmt.Errorf("usage: atto auth set <provider>   (e.g. opencode, opencode-go)")
	}
	provider := args[1]
	var key string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(out, "API key for %s: ", provider)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return err
		}
		key = string(b)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		key = line
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("no key given")
	}
	if err := config.SetAPIKey(provider, key); err != nil {
		return err
	}
	fmt.Fprintf(out, "Saved key for %s to %s\n", provider, config.AuthPath())
	return nil
}

// RunModels implements "atto models [refresh]": list available models.
func RunModels(args []string, out io.Writer) error {
	if len(args) > 0 && args[0] == "refresh" || config.CatalogStale() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := config.RefreshCatalog(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "catalog refresh failed:", err)
		}
	}
	models, err := config.LoadModels()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tNAME\tCONTEXT\tEFFORTS\tKEY")
	for _, r := range models.List() {
		key := "yes"
		if r.APIKey == "" {
			key = "-"
		}
		fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%s\t%s\n", r.ProviderName, r.Model.ID, r.Model.DisplayName(),
			textfmt.Tokens(r.Model.ContextWindow), strings.Join(r.Model.Levels(), ","), key)
	}
	return tw.Flush()
}
