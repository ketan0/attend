package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ketan0/attend/internal/client"
	"github.com/ketan0/attend/internal/rules"
	"github.com/ketan0/attend/internal/server"
)

func newInjectCmd(c func() *client.Client) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inject",
		Short: "Persistent userscript-style page modifications (CSS/JS at document_start).",
		Long: `Inject JS and/or CSS into pages matching a URL pattern, every time
those pages load. Patterns use Chrome's match-pattern syntax, e.g.
"https://*.github.com/*" or "<all_urls>". The browser extension registers
JS injections with chrome.userScripts (requires Chrome Developer Mode
enabled at chrome://extensions) and applies CSS via chrome.scripting on
navigation commit. JS runs in either the page's MAIN realm or an
ISOLATED one, at the requested document lifecycle moment.

Examples:
  attend inject add --match 'https://*.github.com/*' --js-file dark.js
  attend inject add --match '<all_urls>' --css-file fonts.css
  echo 'document.title="hi"' | attend inject add --match 'https://example.com/*' --js-file -
  attend inject ls
  attend inject rm inj_abc123`,
	}
	cmd.AddCommand(
		newInjectAddCmd(c),
		newInjectLsCmd(c),
		newInjectGetCmd(c),
		newInjectRmCmd(c),
	)
	return cmd
}

func newInjectAddCmd(c func() *client.Client) *cobra.Command {
	var (
		match, exclude              []string
		js, css, jsFile, cssFile    string
		name, runAt, world, idFlag  string
		allFrames                   bool
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Create or upsert an injection.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(match) == 0 {
				return errors.New("at least one --match is required")
			}

			jsBody, err := readPayload(cmd, js, jsFile, "--js")
			if err != nil {
				return err
			}
			cssBody, err := readPayload(cmd, css, cssFile, "--css")
			if err != nil {
				return err
			}
			if jsBody == "" && cssBody == "" {
				return errors.New("provide --js/--js-file or --css/--css-file")
			}

			req := server.CreateInjectionRequest{
				ID:        idFlag,
				Name:      name,
				Match:     toMatchPatterns(match),
				Exclude:   toMatchPatterns(exclude),
				RunAt:     rules.RunAt(runAt),
				World:     rules.World(world),
				AllFrames: allFrames,
				JS:        jsBody,
				CSS:       cssBody,
			}
			inj, err := c().CreateInjection(context.Background(), req)
			if err != nil {
				return err
			}
			return emit(cmd, inj)
		},
	}
	cmd.Flags().StringSliceVar(&match, "match", nil, "Chrome match pattern (repeatable, required)")
	cmd.Flags().StringSliceVar(&exclude, "exclude", nil, "exclude match pattern (repeatable)")
	cmd.Flags().StringVar(&js, "js", "", "inline JavaScript to inject")
	cmd.Flags().StringVar(&jsFile, "js-file", "", "path to JS file (use - for stdin)")
	cmd.Flags().StringVar(&css, "css", "", "inline CSS to inject")
	cmd.Flags().StringVar(&cssFile, "css-file", "", "path to CSS file (use - for stdin)")
	cmd.Flags().StringVar(&name, "name", "", "human-readable label")
	cmd.Flags().StringVar(&runAt, "run-at", string(rules.RunAtIdle), "document_start|document_end|document_idle")
	cmd.Flags().StringVar(&world, "world", string(rules.WorldMain), "MAIN|ISOLATED execution world")
	cmd.Flags().BoolVar(&allFrames, "all-frames", false, "inject into all frames (default: top frame only)")
	cmd.Flags().StringVar(&idFlag, "id", "", "stable ID for upsert (default: auto-generated)")
	return cmd
}

func newInjectLsCmd(c func() *client.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List all injections.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out, err := c().ListInjections(context.Background())
			if err != nil {
				return err
			}
			return emit(cmd, out)
		},
	}
}

func newInjectGetCmd(c func() *client.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Get one injection.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inj, err := c().GetInjection(context.Background(), args[0])
			if err != nil {
				return err
			}
			return emit(cmd, inj)
		},
	}
}

func newInjectRmCmd(c func() *client.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id>",
		Short: "Delete an injection.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c().DeleteInjection(context.Background(), args[0]); err != nil {
				return err
			}
			return emit(cmd, map[string]any{"deleted": true, "id": args[0]})
		},
	}
}

// readPayload returns the effective string for a payload flag pair. Exactly
// zero or one of inline/file may be set; "-" for file means read stdin.
func readPayload(cmd *cobra.Command, inline, file, label string) (string, error) {
	if inline != "" && file != "" {
		return "", fmt.Errorf("%s and %s-file are mutually exclusive", label, label)
	}
	if inline != "" {
		return inline, nil
	}
	if file == "" {
		return "", nil
	}
	if file == "-" {
		b, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("%s-file stdin: %w", label, err)
		}
		return string(b), nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("%s-file %s: %w", label, file, err)
	}
	return string(b), nil
}

func toMatchPatterns(in []string) []rules.MatchPattern {
	out := make([]rules.MatchPattern, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, rules.MatchPattern(s))
	}
	return out
}
