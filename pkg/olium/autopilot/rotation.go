package autopilot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vigolium/vigolium/pkg/olium/engine"
)

// Durable-autopilot rotation helpers. These are only reached when a run opts
// into a non-legacy mode with a session dir (rotationEnabled in Run); legacy
// runs never touch them.

// rotationRecentActionsWindow caps how many recent tool actions are carried
// into a section's closing summary + the next section's reconstructed brief.
const rotationRecentActionsWindow = 8

// productiveTools are the tools whose successful execution counts as "progress"
// for the stall detector: they create new attack surface (records), or record a
// result (finding/candidate). A turn that only reads/inspects — or only writes
// bookkeeping — does not reset the stall counter.
//
// Deliberately EXCLUDES update_plan and remember: they mutate the scratchpad but
// make no forward progress against the target, so a weak model that spins
// re-writing its plan every turn would otherwise look "productive" forever and
// never trip the stall rotation (observed: a small model looped on update_plan
// until only the 40-turn cap caught it, instead of the 12-turn stall). Progress
// must mean new surface or a recorded result, not planning about it.
var productiveTools = map[string]bool{
	"run_native_scan":   true,
	"run_extension":     true,
	"run_module":        true,
	"browser_probe":     true,
	"web_fetch":         true,
	"replay_request":    true,
	"send_raw_http":     true,
	"browser_auth":      true,
	"report_finding":    true,
	"propose_candidate": true,
}

// httpBashClients are the command names that, run through bash, put requests on
// the wire. Their presence is what separates a target-facing shell turn from
// bookkeeping.
//
// Deliberately narrow: bare `http`, `nc` and `python` occur in prose, URLs and
// filenames often enough that a substring match on them means nothing.
var httpBashClients = []string{"curl", "wget", "httpie", "httpx", "hurl", "ncat", "netcat"}

// productiveCall reports whether a finished tool call counts as progress for the
// stall detector. It extends productiveTools with the one case the static map
// cannot express: a bash turn that speaks HTTP to an in-scope host.
//
// Without it an operator that verifies through the shell — `curl` with a
// harvested session cookie, say — reads as stalled even though it is probing the
// target, which is exactly the work the detector exists to encourage. The
// command has to name both an HTTP client and one of the run's hosts, so reading
// source, grepping for a URL, or querying the target's database through
// `docker exec` still counts as no progress.
func productiveCall(name string, args map[string]any, hosts []string) bool {
	if productiveTools[name] {
		return true
	}
	if name != "bash" {
		return false
	}
	command, _ := args["command"].(string)
	return bashTargetsHost(command, hosts)
}

// bashTargetsHost reports whether command speaks HTTP to one of hosts. Matching
// is textual on purpose — the command is a shell string, not an argv — so a
// command that only quotes a target URL in a search pattern is filtered out by
// the client-name requirement rather than by parsing.
func bashTargetsHost(command string, hosts []string) bool {
	if command == "" || len(hosts) == 0 {
		return false
	}
	if !containsWord(command, httpBashClients...) {
		return false
	}
	lower := strings.ToLower(command)
	for _, h := range hosts {
		if h != "" && strings.Contains(lower, h) {
			return true
		}
	}
	return false
}

// containsWord reports whether s contains any of words as a whole word, so
// "curl" matches `curl -s` but not `curl_to_json.py`.
func containsWord(s string, words ...string) bool {
	lower := strings.ToLower(s)
	for _, w := range words {
		for idx := 0; idx < len(lower); {
			i := strings.Index(lower[idx:], w)
			if i < 0 {
				break
			}
			start := idx + i
			end := start + len(w)
			if !isWordByte(lower, start-1) && !isWordByte(lower, end) {
				return true
			}
			idx = end
		}
	}
	return false
}

// isWordByte reports whether s[i] is a character that would make a match part of
// a longer word (`_`, `-`, letters, digits).
func isWordByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// progressTargetHosts turns the run's target and scope into the lowercase host
// strings a bash command must name to count as target-facing progress. Loopback
// aliases are included because an operator told `http://localhost` may write
// 127.0.0.1 (or [::1]) in the shell.
func progressTargetHosts(target string, scope []string) []string {
	seen := map[string]bool{}
	var hosts []string
	addHost := func(raw string) {
		primary := normalizeHostString(raw)
		if primary == "" {
			return
		}
		aliases := []string{primary}
		switch primary {
		case "localhost":
			aliases = append(aliases, "127.0.0.1", "[::1]")
		case "127.0.0.1":
			aliases = append(aliases, "localhost", "[::1]")
		}
		for _, a := range aliases {
			if !seen[a] {
				seen[a] = true
				hosts = append(hosts, a)
			}
		}
	}
	addHost(target)
	for _, s := range scope {
		addHost(s)
	}
	return hosts
}

// normalizeHostString reduces a target URL or scope pattern to the substring a
// shell command would carry: no scheme, no userinfo, no port, no path, no
// wildcard prefix.
func normalizeHostString(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, ":"); i > 0 && allDigits(s[i+1:]) {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "*.")
	s = strings.TrimPrefix(s, ".")
	return strings.TrimSuffix(s, ".")
}

// allDigits reports whether s is a non-empty run of ASCII digits (a port).
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// buildRotationMission assembles the compact mission line reused at the top of
// every reconstructed brief. The heavy detail (plan, notes, stop criteria)
// lives in the durable scratchpad, so this stays short — the whole point of
// rotation is not to re-send an ever-growing prefix.
func buildRotationMission(opts Options) string {
	var b strings.Builder
	b.WriteString("# Autonomous security assessment (continuing in a fresh section)\n")
	if opts.Target != "" {
		fmt.Fprintf(&b, "Target: %s\n", opts.Target)
	}
	if opts.SourcePath != "" {
		fmt.Fprintf(&b, "Source: %s\n", opts.SourcePath)
	}
	if opts.Focus != "" {
		fmt.Fprintf(&b, "Focus: %s\n", opts.Focus)
	}
	if len(opts.Scope) > 0 {
		fmt.Fprintf(&b, "Scope: %s\n", strings.Join(opts.Scope, ", "))
	}
	if strings.TrimSpace(opts.Instruction) != "" {
		fmt.Fprintf(&b, "Operator instruction: %s\n", truncate(oneLine(opts.Instruction), 400))
	}
	b.WriteString("Work the plan below. When its stop criteria are met, call halt_scan with a summary.")
	return b.String()
}

// appendRecentAction records a one-line summary of a tool execution into the
// bounded recent-actions ring, returning the updated slice.
func appendRecentAction(entries []string, toolName, result string) []string {
	entry := toolName
	if r := strings.TrimSpace(result); r != "" {
		entry += ": " + truncate(oneLine(r), 120)
	}
	entries = append(entries, entry)
	if len(entries) > rotationRecentActionsWindow {
		entries = entries[len(entries)-rotationRecentActionsWindow:]
	}
	return entries
}

// renderRecentActions joins the recent-actions ring into a bulleted block, or
// "" when empty.
func renderRecentActions(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString("- ")
		b.WriteString(e)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// summarizeSection runs a single cheap, tool-less model call to condense the
// closing state of a section (what it established, what's still open, the one
// most important next step). Used at each rotation to write a durable closing
// summary that seeds the next section's brief. Best-effort: any provider error
// or a nil provider returns "", and the reconstructed brief still carries the
// full working memory.
func summarizeSection(ctx context.Context, opts Options, scratch *ScratchpadContext, recentActions string) string {
	if opts.Provider == nil {
		return ""
	}
	const sys = "Summarize what this section established, what is still open, and the single most important next step. 3-6 bullet lines."
	eng := engine.New(engine.Config{
		Provider: opts.Provider,
		Model:    opts.Model,
		System:   sys,
		MaxTurns: 1,
	})

	var user strings.Builder
	if scratch != nil {
		user.WriteString(scratch.Render())
	}
	if strings.TrimSpace(recentActions) != "" {
		user.WriteString("\n\nRecent actions:\n")
		user.WriteString(recentActions)
	}
	if strings.TrimSpace(user.String()) == "" {
		return ""
	}

	sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	var text strings.Builder
	for ev := range eng.Run(sctx, user.String()) {
		if ev.Type == engine.EventTextDelta {
			text.WriteString(ev.Delta)
		}
	}
	return strings.TrimSpace(text.String())
}

// oneLine collapses all runs of whitespace (including newlines) to single
// spaces so a multi-line tool result folds into one readable log/brief entry.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
