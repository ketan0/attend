package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ketan0/attend/internal/client"
	"github.com/ketan0/attend/internal/server"
)

// tabSelector mirrors what the extension expects in job payloads.
type tabSelector struct {
	Active     bool   `json:"active,omitempty"`
	TabID      int    `json:"tab_id,omitempty"`
	URLPattern string `json:"url_pattern,omitempty"`
}

// selectorFlags binds --active / --tab-id / --url-pattern and produces a
// tabSelector. Default (no flags) is --active.
type selectorFlags struct {
	active     bool
	tabID      int
	urlPattern string
}

func (sf *selectorFlags) attach(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&sf.active, "active", false, "target the active tab in the focused window (default)")
	cmd.Flags().IntVar(&sf.tabID, "tab-id", 0, "target a specific tab id (see attend page tabs)")
	cmd.Flags().StringVar(&sf.urlPattern, "url-pattern", "", "target the first tab whose URL matches this Chrome match pattern")
}

func (sf *selectorFlags) build() (tabSelector, error) {
	count := 0
	if sf.active {
		count++
	}
	if sf.tabID != 0 {
		count++
	}
	if sf.urlPattern != "" {
		count++
	}
	if count > 1 {
		return tabSelector{}, errors.New("specify at most one of --active/--tab-id/--url-pattern")
	}
	if count == 0 {
		return tabSelector{Active: true}, nil
	}
	if sf.urlPattern != "" {
		return tabSelector{URLPattern: sf.urlPattern}, nil
	}
	if sf.tabID != 0 {
		return tabSelector{TabID: sf.tabID}, nil
	}
	return tabSelector{Active: true}, nil
}

func newPageCmd(c func() *client.Client) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "page",
		Short: "Inspect and act on live browser tabs (ephemeral RPC into the extension).",
		Long: `Ephemeral page RPC: list tabs, dump the HTML of a tab to a temp file,
or run a one-shot JavaScript expression in a tab and return its value.

These commands block on the daemon → extension round-trip and time out
(default 30s) if the extension isn't reachable. Selector flags
(--active|--tab-id|--url-pattern) pick which tab to target; default is
--active (the focused tab in the focused window).`,
	}
	cmd.AddCommand(
		newPageTabsCmd(c),
		newPageDumpCmd(c),
		newPageExecCmd(c),
	)
	return cmd
}

func submitPageJob(c *client.Client, kind string, payload any, timeout string) (json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	// Add a small slack on top of the daemon timeout so the HTTP client
	// doesn't trip first.
	ctxTimeout := 35 * time.Second
	if timeout != "" {
		if d, err := time.ParseDuration(timeout); err == nil {
			ctxTimeout = d + 5*time.Second
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctxTimeout)
	defer cancel()

	result, err := c.SubmitPageJob(ctx, server.SubmitPageJobRequest{
		Kind:    kind,
		Payload: raw,
		Timeout: timeout,
	})
	if err != nil {
		return nil, err
	}
	if !result.Ok {
		msg := result.Error
		if msg == "" {
			msg = "extension returned an unspecified error"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return result.Value, nil
}

// --- page tabs --------------------------------------------------------------

func newPageTabsCmd(c func() *client.Client) *cobra.Command {
	var timeout string
	cmd := &cobra.Command{
		Use:   "tabs",
		Short: "List open browser tabs.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			value, err := submitPageJob(c(), "tabs.list", map[string]any{}, timeout)
			if err != nil {
				return err
			}
			// Pretty-pass through.
			var raw any
			if err := json.Unmarshal(value, &raw); err != nil {
				return fmt.Errorf("decode tabs: %w", err)
			}
			return emit(cmd, raw)
		},
	}
	cmd.Flags().StringVar(&timeout, "timeout", "10s", "fail if the extension doesn't respond within this duration")
	return cmd
}

// --- page dump --------------------------------------------------------------

type pageDumpResult struct {
	TabID int    `json:"tab_id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	HTML  string `json:"html"`
}

func newPageDumpCmd(c func() *client.Client) *cobra.Command {
	var sel selectorFlags
	var outFile, timeout string
	cmd := &cobra.Command{
		Use:   "dump",
		Short: "Dump the outerHTML of a tab to a temp file.",
		Long: `Writes the page's full outerHTML to a temp file (default:
/tmp/attend-page-<tab_id>-<unix_ts>.html) and prints
{tab_id, url, title, file, bytes} on stdout. HTML can be megabytes; agents
should grep/sed/jq the file rather than slurping it into context.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ts, err := sel.build()
			if err != nil {
				return err
			}
			value, err := submitPageJob(c(), "page.dump", map[string]any{"tab": ts}, timeout)
			if err != nil {
				return err
			}
			var dump pageDumpResult
			if err := json.Unmarshal(value, &dump); err != nil {
				return fmt.Errorf("decode dump: %w", err)
			}
			path := outFile
			if path == "" {
				path = filepath.Join(os.TempDir(),
					fmt.Sprintf("attend-page-%d-%d.html", dump.TabID, time.Now().Unix()))
			}
			if err := os.WriteFile(path, []byte(dump.HTML), 0o600); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			return emit(cmd, map[string]any{
				"tab_id": dump.TabID,
				"url":    dump.URL,
				"title":  dump.Title,
				"file":   path,
				"bytes":  len(dump.HTML),
			})
		},
	}
	sel.attach(cmd)
	cmd.Flags().StringVar(&outFile, "out", "", "explicit output path (default: /tmp/attend-page-<id>-<ts>.html)")
	cmd.Flags().StringVar(&timeout, "timeout", "30s", "fail if the extension doesn't respond within this duration")
	return cmd
}

// --- page exec --------------------------------------------------------------

type pageExecResult struct {
	TabID int             `json:"tab_id"`
	URL   string          `json:"url"`
	Value json.RawMessage `json:"value"`
}

func newPageExecCmd(c func() *client.Client) *cobra.Command {
	var sel selectorFlags
	var js, jsFile, world, timeout string
	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Run JS in a tab and print its return value.",
		Long: `Executes a one-shot JavaScript expression or function body in a live
tab via chrome.scripting.executeScript. The expression's return value (or
the function's return) is JSON-serialized and printed.

Examples:
  attend page exec --js 'document.title'
  attend page exec --js 'document.querySelectorAll("a").length'
  attend page exec --js-file ./snippet.js --url-pattern 'https://github.com/*'

Note: the value must be JSON-serializable. DOM nodes and functions are not;
project them to plain objects/strings before returning.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := readPayload(cmd, js, jsFile, "--js")
			if err != nil {
				return err
			}
			if strings.TrimSpace(body) == "" {
				return errors.New("provide --js or --js-file")
			}
			ts, err := sel.build()
			if err != nil {
				return err
			}
			payload := map[string]any{"tab": ts, "code": body, "world": world}
			value, err := submitPageJob(c(), "page.exec", payload, timeout)
			if err != nil {
				return err
			}
			var out pageExecResult
			if err := json.Unmarshal(value, &out); err != nil {
				return fmt.Errorf("decode exec result: %w", err)
			}
			// Print the actual value, not the envelope — agents care about
			// what the JS returned, not the tab metadata.
			var v any
			if len(out.Value) == 0 {
				v = nil
			} else if err := json.Unmarshal(out.Value, &v); err != nil {
				v = string(out.Value)
			}
			return emit(cmd, map[string]any{
				"tab_id": out.TabID,
				"url":    out.URL,
				"value":  v,
			})
		},
	}
	sel.attach(cmd)
	cmd.Flags().StringVar(&js, "js", "", "inline JavaScript expression or function body")
	cmd.Flags().StringVar(&jsFile, "js-file", "", "path to JS file (use - for stdin)")
	cmd.Flags().StringVar(&world, "world", "MAIN", "MAIN|ISOLATED execution world")
	cmd.Flags().StringVar(&timeout, "timeout", "30s", "fail if the extension doesn't respond within this duration")
	return cmd
}
