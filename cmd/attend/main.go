// attend is the CLI client for attendd.
//
// All commands emit JSON on stdout (so agents can parse), and structured
// errors as JSON on stderr (with non-zero exit codes).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ketan0/attend/internal/client"
	"github.com/ketan0/attend/internal/rules"
	"github.com/ketan0/attend/internal/server"
)

const defaultBaseURL = "http://127.0.0.1:7723"

func main() {
	if err := newRoot().Execute(); err != nil {
		// Cobra already printed the error.
		os.Exit(exitCode(err))
	}
}

// exitCode maps an error to a CLI exit code:
//   1 — user error / validation
//   2 — daemon unreachable / system error
func exitCode(err error) int {
	var ae *client.APIError
	if errors.As(err, &ae) {
		if ae.Status >= 500 {
			return 2
		}
		return 1
	}
	return 2
}

func newRoot() *cobra.Command {
	var baseURL string
	root := &cobra.Command{
		Use:   "attend",
		Short: "Shape and guide your attention by creating block/nudge/friction rules.",
		// Agents parse stdout/stderr; cobra's usage spam on every error is
		// noise. Errors are still printed (cobra prints "Error: ..." once).
		SilenceUsage: true,
	}
	root.PersistentFlags().StringVar(&baseURL, "url", defaultBaseURL, "attendd base URL")

	apiClient := func() *client.Client { return client.New(baseURL) }

	root.AddCommand(
		newBlockCmd(apiClient),
		newAllowCmd(apiClient),
		newFrictionCmd(apiClient),
		newNudgeCmd(apiClient),
		newLsCmd(apiClient),
		newGetCmd(apiClient),
		newRmCmd(apiClient),
		newUpdateCmd(apiClient),
		newPauseCmd(apiClient),
		newResumeCmd(apiClient),
		newStatusCmd(apiClient),
	)
	return root
}

// --- shared helpers ---------------------------------------------------------

// scopeFlags binds --for/--until/--schedule-json/--schedule-file to a request.
type scopeFlags struct {
	For          string
	Until        string
	ScheduleJSON string
	ScheduleFile string
}

func (sf *scopeFlags) attach(cmd *cobra.Command) {
	cmd.Flags().StringVar(&sf.For, "for", "", "active for this duration (e.g. 2h, 30m)")
	cmd.Flags().StringVar(&sf.Until, "until", "", "active until this RFC3339 timestamp")
	cmd.Flags().StringVar(&sf.ScheduleJSON, "schedule-json", "", "recurring schedule (JSON object)")
	cmd.Flags().StringVar(&sf.ScheduleFile, "schedule-file", "", "recurring schedule file (JSON)")
}

func (sf *scopeFlags) apply(req *server.CreateRuleRequest) error {
	count := 0
	if sf.For != "" {
		count++
	}
	if sf.Until != "" {
		count++
	}
	if sf.ScheduleJSON != "" {
		count++
	}
	if sf.ScheduleFile != "" {
		count++
	}
	if count > 1 {
		return errors.New("specify at most one of --for/--until/--schedule-json/--schedule-file")
	}
	switch {
	case sf.For != "":
		req.For = sf.For
	case sf.Until != "":
		t, err := time.Parse(time.RFC3339, sf.Until)
		if err != nil {
			return fmt.Errorf("--until: %w", err)
		}
		req.Until = &t
	case sf.ScheduleJSON != "":
		var rec rules.RecurringSchedule
		if err := json.Unmarshal([]byte(sf.ScheduleJSON), &rec); err != nil {
			return fmt.Errorf("--schedule-json: %w", err)
		}
		req.Recurring = &rec
	case sf.ScheduleFile != "":
		raw, err := os.ReadFile(sf.ScheduleFile)
		if err != nil {
			return fmt.Errorf("--schedule-file: %w", err)
		}
		var rec rules.RecurringSchedule
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("--schedule-file: %w", err)
		}
		req.Recurring = &rec
	}
	return nil
}

// parseTarget converts a CLI target string into a Target. Path = contains "/",
// app = starts with "app:", domain = otherwise.
func parseTarget(s string) (rules.Target, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return rules.Target{}, errors.New("target is required")
	}
	if strings.HasPrefix(s, "app:") {
		v := strings.TrimSpace(strings.TrimPrefix(s, "app:"))
		if v == "" {
			return rules.Target{}, errors.New("app target is empty")
		}
		return rules.Target{Kind: rules.TargetApp, Value: v}, nil
	}
	if strings.HasPrefix(s, "domain:") {
		return rules.Target{Kind: rules.TargetDomain, Value: strings.TrimPrefix(s, "domain:")}, nil
	}
	if strings.HasPrefix(s, "path:") {
		return rules.Target{Kind: rules.TargetPath, Value: strings.TrimPrefix(s, "path:")}, nil
	}
	if strings.Contains(s, "/") {
		return rules.Target{Kind: rules.TargetPath, Value: s}, nil
	}
	return rules.Target{Kind: rules.TargetDomain, Value: s}, nil
}

func emit(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// --- block ------------------------------------------------------------------

func newBlockCmd(c func() *client.Client) *cobra.Command {
	var sf scopeFlags
	var replace bool
	cmd := &cobra.Command{
		Use:   "block <target>",
		Short: "Hard-block a target (no override).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tgt, err := parseTarget(args[0])
			if err != nil {
				return err
			}
			req := server.CreateRuleRequest{
				Action:  rules.ActionBlock,
				Target:  tgt,
				Replace: replace,
			}
			if err := sf.apply(&req); err != nil {
				return err
			}
			ctx := context.Background()
			r, err := c().CreateRule(ctx, req)
			if err != nil {
				return err
			}
			return emit(cmd, r)
		},
	}
	sf.attach(cmd)
	cmd.Flags().BoolVar(&replace, "replace", false, "overwrite an existing rule on the same target")
	return cmd
}

// --- allow ------------------------------------------------------------------

func newAllowCmd(c func() *client.Client) *cobra.Command {
	var sf scopeFlags
	var replace bool
	cmd := &cobra.Command{
		Use:   "allow <target>",
		Short: "Carve out an exception from broader block/friction rules.",
		Long: `Allow rules suppress all other matching rules. Use to carve narrow
exceptions out of broader blocks, e.g.:

  attend block reddit.com
  attend allow reddit.com/r/LocalLLaMA

Note: domain-level allow only takes effect in the browser extension. The
system-wide /etc/hosts block is host-level only — to allow a path under a
blocked domain, you need the extension installed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tgt, err := parseTarget(args[0])
			if err != nil {
				return err
			}
			req := server.CreateRuleRequest{
				Action:  rules.ActionAllow,
				Target:  tgt,
				Replace: replace,
			}
			if err := sf.apply(&req); err != nil {
				return err
			}
			r, err := c().CreateRule(context.Background(), req)
			if err != nil {
				return err
			}
			return emit(cmd, r)
		},
	}
	sf.attach(cmd)
	cmd.Flags().BoolVar(&replace, "replace", false, "overwrite an existing rule on the same target")
	return cmd
}

// --- friction ---------------------------------------------------------------

func newFrictionCmd(c func() *client.Client) *cobra.Command {
	var sf scopeFlags
	var level string
	var replace bool
	var cooldown string
	var phrase string
	var timer int
	cmd := &cobra.Command{
		Use:   "friction <target>",
		Short: "Interpose a challenge before allowing access.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tgt, err := parseTarget(args[0])
			if err != nil {
				return err
			}
			fc := &rules.FrictionConfig{
				Level:        rules.FrictionLevel(level),
				TimerSeconds: timer,
				Phrase:       phrase,
			}
			if cooldown != "" {
				d, err := time.ParseDuration(cooldown)
				if err != nil {
					return fmt.Errorf("--cooldown: %w", err)
				}
				fc.Cooldown = rules.Duration(d)
			}
			req := server.CreateRuleRequest{
				Action:   rules.ActionFriction,
				Target:   tgt,
				Friction: fc,
				Replace:  replace,
			}
			if err := sf.apply(&req); err != nil {
				return err
			}
			r, err := c().CreateRule(context.Background(), req)
			if err != nil {
				return err
			}
			return emit(cmd, r)
		},
	}
	sf.attach(cmd)
	cmd.Flags().StringVar(&level, "level", "intent", "friction level: timer|intent|phrase|math|breath")
	cmd.Flags().StringVar(&cooldown, "cooldown", "5m", "after a passed challenge, target stays unlocked this long")
	cmd.Flags().StringVar(&phrase, "phrase", "", "for level=phrase: the phrase the user must type")
	cmd.Flags().IntVar(&timer, "timer-seconds", 0, "for level=timer or level=breath: countdown in seconds")
	cmd.Flags().BoolVar(&replace, "replace", false, "overwrite an existing rule on the same target")
	return cmd
}

// --- nudge ------------------------------------------------------------------

func newNudgeCmd(c func() *client.Client) *cobra.Command {
	var sf scopeFlags
	var replace bool
	var message string
	cmd := &cobra.Command{
		Use:   "nudge <target>",
		Short: "Send a notification when target is engaged (no enforcement).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tgt, err := parseTarget(args[0])
			if err != nil {
				return err
			}
			if strings.TrimSpace(message) == "" {
				return errors.New("--message is required for nudge")
			}
			req := server.CreateRuleRequest{
				Action:  rules.ActionNudge,
				Target:  tgt,
				Message: message,
				Replace: replace,
			}
			if err := sf.apply(&req); err != nil {
				return err
			}
			r, err := c().CreateRule(context.Background(), req)
			if err != nil {
				return err
			}
			return emit(cmd, r)
		},
	}
	sf.attach(cmd)
	cmd.Flags().StringVar(&message, "message", "", "notification message")
	cmd.Flags().BoolVar(&replace, "replace", false, "overwrite an existing rule on the same target")
	return cmd
}

// --- read commands ----------------------------------------------------------

func newLsCmd(c func() *client.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List all rules.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rs, err := c().ListRules(context.Background())
			if err != nil {
				return err
			}
			return emit(cmd, rs)
		},
	}
}

func newGetCmd(c func() *client.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Get one rule.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := c().GetRule(context.Background(), args[0])
			if err != nil {
				return err
			}
			return emit(cmd, r)
		},
	}
}

func newRmCmd(c func() *client.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id>",
		Short: "Delete a rule.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c().DeleteRule(context.Background(), args[0]); err != nil {
				return err
			}
			return emit(cmd, map[string]any{"deleted": true, "id": args[0]})
		},
	}
}

// --- update -----------------------------------------------------------------

func newUpdateCmd(c func() *client.Client) *cobra.Command {
	var forStr, untilStr, message, scheduleJSON string
	var alwaysFlag, hasAlways bool
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Patch fields on an existing rule (only flags you pass are applied).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := server.UpdateRuleRequest{}
			if cmd.Flags().Changed("for") {
				req.For = &forStr
			}
			if cmd.Flags().Changed("until") {
				t, err := time.Parse(time.RFC3339, untilStr)
				if err != nil {
					return fmt.Errorf("--until: %w", err)
				}
				req.Until = &t
			}
			if cmd.Flags().Changed("message") {
				req.Message = &message
			}
			if cmd.Flags().Changed("always") {
				hasAlways = true
				req.Always = &alwaysFlag
			}
			_ = hasAlways
			if cmd.Flags().Changed("schedule-json") {
				var rec rules.RecurringSchedule
				if err := json.Unmarshal([]byte(scheduleJSON), &rec); err != nil {
					return fmt.Errorf("--schedule-json: %w", err)
				}
				req.Recurring = &rec
			}
			r, err := c().UpdateRule(context.Background(), args[0], req)
			if err != nil {
				return err
			}
			return emit(cmd, r)
		},
	}
	cmd.Flags().StringVar(&forStr, "for", "", "set schedule to active-for-duration")
	cmd.Flags().StringVar(&untilStr, "until", "", "set schedule to active-until-RFC3339")
	cmd.Flags().StringVar(&message, "message", "", "update nudge message")
	cmd.Flags().StringVar(&scheduleJSON, "schedule-json", "", "set recurring schedule")
	cmd.Flags().BoolVar(&alwaysFlag, "always", false, "set schedule to always-on")
	return cmd
}

// --- pause / resume / status ------------------------------------------------

func newPauseCmd(c func() *client.Client) *cobra.Command {
	var forStr, untilStr string
	cmd := &cobra.Command{
		Use:   "pause",
		Short: "Suppress all enforcement (optionally for a duration).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := server.PauseRequest{}
			if forStr != "" {
				req.For = forStr
			}
			if untilStr != "" {
				t, err := time.Parse(time.RFC3339, untilStr)
				if err != nil {
					return fmt.Errorf("--until: %w", err)
				}
				req.Until = &t
			}
			s, err := c().Pause(context.Background(), req)
			if err != nil {
				return err
			}
			return emit(cmd, s)
		},
	}
	cmd.Flags().StringVar(&forStr, "for", "", "pause for this duration (e.g. 30m)")
	cmd.Flags().StringVar(&untilStr, "until", "", "pause until this RFC3339 timestamp")
	return cmd
}

func newResumeCmd(c func() *client.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "resume",
		Short: "Re-enable enforcement.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := c().Resume(context.Background())
			if err != nil {
				return err
			}
			return emit(cmd, s)
		},
	}
}

func newStatusCmd(c func() *client.Client) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon status, rules, and what's active right now.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := c().Status(context.Background())
			if err != nil {
				return err
			}
			return emit(cmd, s)
		},
	}
}

// guard against unused-import error if we drop http use in future iterations.
var _ = http.StatusOK
