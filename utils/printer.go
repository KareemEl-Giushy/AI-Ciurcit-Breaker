package utils

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// PrintToolCall contains tool invocation information formatted for terminal display.
type PrintToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// PrintMessage contains chat message information formatted for terminal display.
type PrintMessage struct {
	Role         string
	Content      string
	ToolCallID   string
	ToolCalls    []PrintToolCall
	FunctionName string
	FunctionArgs string
}

// SREViolatingCall contains tool and argument details for a violating invocation.
type SREViolatingCall struct {
	ToolName  string
	Arguments string
	Error     string
	Distance  int
	Score     float64
}

// SREIncidentSummary contains incident details for formatted terminal alerts.
type SREIncidentSummary struct {
	IncidentID      string
	FailureClass    string
	ToolName        string
	ToolArguments   string
	ViolatingCalls  []SREViolatingCall
	Fingerprint     string
	SimHashHex      string
	HammingDistance int
	SimilarityScore float64
	ObservedCount   int
	Threshold       int
	Mitigation      string
	ActionRequired  string
}

// PrintStartupBanner outputs the stylized ASCII application banner and active configuration settings.
func PrintStartupBanner(port, target string, window time.Duration, maxReq, maxTok int, enforce bool, cbMaxToolRepeats, cbMaxToolErrors, cbHamming int, cbJaccard float64, maxRPS float64, maxEndpointRepeats int, repeatWindow time.Duration, saveConv bool, saveDir string) {
	fmt.Printf("\n%s%s╔══════════════════════════════════════════════════════════════════════════════╗%s\n", ColorCyan, ColorBold, ColorReset)
	fmt.Printf("%s%s║                ⚡ CIRCUIT BREAKER REVERSE PROXY & INSPECTOR                 ║%s\n", ColorCyan, ColorBold, ColorReset)
	fmt.Printf("%s%s╚══════════════════════════════════════════════════════════════════════════════╝%s\n", ColorCyan, ColorBold, ColorReset)
	fmt.Printf("  %s• Listening on       :%s %s:%s%s\n", ColorBold, ColorReset, ColorGreen, port, ColorReset)
	fmt.Printf("  %s• Forwarding to      :%s %s%s%s\n", ColorBold, ColorReset, ColorBlue, target, ColorReset)
	fmt.Printf("  %s• Sliding Window     :%s %s%s%s\n", ColorBold, ColorReset, ColorYellow, window, ColorReset)
	fmt.Printf("  %s• Max Window Requests:%s %d (0 = unlimited)\n", ColorBold, ColorReset, maxReq)
	fmt.Printf("  %s• Max Window Tokens  :%s %d (0 = unlimited)\n", ColorBold, ColorReset, maxTok)
	fmt.Printf("  %s• Limit Enforcement  :%s %t\n", ColorBold, ColorReset, enforce)
	fmt.Printf("  %s• CB Tool Loop Limit :%s %d identical/similar calls\n", ColorBold, ColorReset, cbMaxToolRepeats)
	fmt.Printf("  %s• CB SimHash Max Dist:%s %d bits (64-bit LSH)\n", ColorBold, ColorReset, cbHamming)
	fmt.Printf("  %s• CB Jaccard Thresh  :%s %.2f similarity index\n", ColorBold, ColorReset, cbJaccard)
	fmt.Printf("  %s• CB Tool Error Limit:%s %d accumulated errors\n", ColorBold, ColorReset, cbMaxToolErrors)
	fmt.Printf("  %s• Velocity Max RPS   :%s %.1f req/s (0 = unlimited)\n", ColorBold, ColorReset, maxRPS)
	fmt.Printf("  %s• Velocity Max Repeat:%s %d hits in %s\n", ColorBold, ColorReset, maxEndpointRepeats, repeatWindow)
	fmt.Printf("  %s• Save Conversations :%s %t (dir: %s)\n", ColorBold, ColorReset, saveConv, saveDir)
	fmt.Printf("%s────────────────────────────────────────────────────────────────────────────────%s\n\n", ColorGray, ColorReset)
}

// FormatMethod returns an ANSI color-coded string for the given HTTP method.
func FormatMethod(method string) string {
	switch method {
	case http.MethodGet:
		return fmt.Sprintf("%s%s%-6s%s", ColorBlue, ColorBold, method, ColorReset)
	case http.MethodPost:
		return fmt.Sprintf("%s%s%-6s%s", ColorGreen, ColorBold, method, ColorReset)
	case http.MethodDelete:
		return fmt.Sprintf("%s%s%-6s%s", ColorRed, ColorBold, method, ColorReset)
	default:
		return fmt.Sprintf("%s%s%-6s%s", ColorYellow, ColorBold, method, ColorReset)
	}
}

// FormatStatus returns an ANSI color-coded string for the HTTP status code and text.
func FormatStatus(code int) string {
	switch {
	case code >= 200 && code < 300:
		return fmt.Sprintf("%s%s%d %s%s", ColorGreen, ColorBold, code, http.StatusText(code), ColorReset)
	case code >= 400 && code < 500:
		return fmt.Sprintf("%s%s%d %s%s", ColorYellow, ColorBold, code, http.StatusText(code), ColorReset)
	default:
		return fmt.Sprintf("%s%s%d %s%s", ColorRed, ColorBold, code, http.StatusText(code), ColorReset)
	}
}

// PrintRequestSummary prints a clean, single-line log for a completed HTTP proxy request.
func PrintRequestSummary(method, uri string, statusCode int, duration time.Duration, model string, msgCount int, estTokens int, winReqs int, winToks int) {
	timestamp := time.Now().Format("15:04:05")

	extraInfo := ""
	if model != "" || msgCount > 0 {
		extraInfo = fmt.Sprintf(" %s[%s | %d msgs | ~%d tokens]%s",
			ColorMagenta, model, msgCount, estTokens, ColorReset)
	}

	windowInfo := fmt.Sprintf("%s[window: %d reqs | ~%d toks]%s",
		ColorCyan, winReqs, winToks, ColorReset)

	fmt.Printf("%s[%s]%s %s %-25s %s %s(%s)%s%s %s\n",
		ColorGray, timestamp, ColorReset,
		FormatMethod(method), uri,
		FormatStatus(statusCode),
		ColorDim, duration.Round(time.Millisecond), ColorReset,
		extraInfo, windowInfo,
	)
}

// PrintBlockedSummary prints a single-line log for a request rejected by sliding window rate limits.
func PrintBlockedSummary(method, uri string, reason string) {
	timestamp := time.Now().Format("15:04:05")
	fmt.Printf("%s[%s]%s %s %-25s %s %s[sliding window blocked: %s]%s\n",
		ColorGray, timestamp, ColorReset,
		FormatMethod(method), uri,
		FormatStatus(http.StatusTooManyRequests),
		ColorYellow, reason, ColorReset,
	)
}

// PrintSavedJSON prints the confirmation message after saving a conversation JSON record.
func PrintSavedJSON(path string) {
	fmt.Printf("  %s💾 Saved conversation JSON:%s %s\n", ColorGray, ColorReset, path)
}

// PrintSREIncident prints a stylized red alert box for a tripped Circuit Breaker incident.
func PrintSREIncident(title string, incident SREIncidentSummary) {
	fmt.Printf("\n%s%s╭── 🚨 SRE INCIDENT: %s ───────────%s\n", ColorRed, ColorBold, title, ColorReset)
	fmt.Printf("%s│%s • Incident ID   : %s\n", ColorRed, ColorReset, incident.IncidentID)
	fmt.Printf("%s│%s • Failure Class : %s%s%s\n", ColorRed, ColorReset, ColorYellow, incident.FailureClass, ColorReset)
	if incident.ToolName != "" {
		fmt.Printf("%s│%s • Trigger Tool  : %s%s%s\n", ColorRed, ColorReset, ColorCyan+ColorBold, incident.ToolName, ColorReset)
	}
	if incident.ToolArguments != "" {
		argsSnippet := strings.TrimSpace(incident.ToolArguments)
		if len(argsSnippet) > 120 {
			argsSnippet = argsSnippet[:117] + "..."
		}
		fmt.Printf("%s│%s • Trigger Args  : %s%s%s\n", ColorRed, ColorReset, ColorWhite, argsSnippet, ColorReset)
	}
	if len(incident.ViolatingCalls) > 0 {
		fmt.Printf("%s│%s • Violating Calls (%d):\n", ColorRed, ColorReset, len(incident.ViolatingCalls))
		for i, vc := range incident.ViolatingCalls {
			argsSnippet := strings.TrimSpace(vc.Arguments)
			if len(argsSnippet) > 90 {
				argsSnippet = argsSnippet[:87] + "..."
			}
			extra := ""
			if vc.Distance > 0 || vc.Score > 0 {
				extra = fmt.Sprintf(" [SimHash Dist: %d, Sim: %.2f]", vc.Distance, vc.Score)
			} else if vc.Error != "" {
				extra = fmt.Sprintf(" [Error: %s]", vc.Error)
			}
			fmt.Printf("%s│%s   %s[%d]%s %s%s%s(args: %s%s%s)%s%s\n",
				ColorRed, ColorReset,
				ColorYellow, i+1, ColorReset,
				ColorCyan, vc.ToolName, ColorYellow+ColorBold,
				ColorWhite, argsSnippet, ColorYellow+ColorBold,
				ColorReset, extra,
			)
		}
	}
	if incident.SimHashHex != "" {
		fmt.Printf("%s│%s • SimHash (LSH) : %s (Hamming Dist: %d bits)\n", ColorRed, ColorReset, incident.SimHashHex, incident.HammingDistance)
	} else if incident.Fingerprint != "" {
		fmt.Printf("%s│%s • Fingerprint   : %s\n", ColorRed, ColorReset, incident.Fingerprint)
	}
	if incident.SimilarityScore > 0 {
		fmt.Printf("%s│%s • Similarity    : %.2f\n", ColorRed, ColorReset, incident.SimilarityScore)
	}
	fmt.Printf("%s│%s • Observed Count: %d (Threshold: %d)\n", ColorRed, ColorReset, incident.ObservedCount, incident.Threshold)
	fmt.Printf("%s│%s • Mitigation    : %s\n", ColorRed, ColorReset, incident.Mitigation)
	if incident.ActionRequired != "" {
		fmt.Printf("%s│%s • Action Req.   : %s\n", ColorRed, ColorReset, incident.ActionRequired)
	}
	fmt.Printf("%s╰────────────────────────────────────────────────────────────────────────%s\n\n", ColorRed, ColorReset)
}

// PrintConversation formats and pretty-prints the recent interaction turn to stdout.
func PrintConversation(path, model string, hiddenCount int, recentMessages []PrintMessage) {
	if len(recentMessages) == 0 {
		return
	}

	if model == "" {
		model = "openai-model"
	}

	if hiddenCount > 0 {
		fmt.Printf("\n%s%s╭── 💬 Last Interaction [%s | %s | %d previous turns hidden] ────────%s\n",
			ColorBlue, ColorBold, path, model, hiddenCount, ColorReset)
	} else {
		fmt.Printf("\n%s%s╭── 💬 Last Interaction [%s | %s] ────────────────────────────────────%s\n",
			ColorBlue, ColorBold, path, model, ColorReset)
	}

	for _, msg := range recentMessages {
		roleBadge := ""
		roleColor := ColorReset

		switch strings.ToLower(msg.Role) {
		case "system":
			roleBadge = "⚙️  [system]   "
			roleColor = ColorDim + ColorGray
		case "user":
			roleBadge = "👤 [user]     "
			roleColor = ColorCyan + ColorBold
		case "assistant":
			roleBadge = "🤖 [assistant]"
			roleColor = ColorGreen
		case "tool":
			if msg.ToolCallID != "" {
				roleBadge = fmt.Sprintf("🛠️  [tool:%s]", msg.ToolCallID)
			} else {
				roleBadge = "🛠️  [tool]     "
			}
			roleColor = ColorMagenta
		case "function":
			roleBadge = "🛠️  [function] "
			roleColor = ColorMagenta
		default:
			roleBadge = fmt.Sprintf("❓ [%s]   ", msg.Role)
			roleColor = ColorYellow
		}

		if msg.Content != "" {
			lines := strings.Split(msg.Content, "\n")
			for i, line := range lines {
				if i == 0 {
					fmt.Printf("%s│%s %s%s%s %s\n", ColorBlue, ColorReset, roleColor, roleBadge, ColorReset, line)
				} else {
					fmt.Printf("%s│%s               %s\n", ColorBlue, ColorReset, line)
				}
			}
		}

		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				fmt.Printf("%s│%s   %s↳ 🛠️  Tool Call: %s%s%s(%s%s%s) [ID: %s]%s\n",
					ColorBlue, ColorReset,
					ColorYellow+ColorBold,
					ColorCyan, tc.Name, ColorYellow+ColorBold,
					ColorWhite, tc.Arguments, ColorYellow+ColorBold,
					tc.ID, ColorReset)
			}
		}

		if msg.FunctionName != "" {
			fmt.Printf("%s│%s   %s↳ 🛠️  Function Call: %s%s%s(%s%s%s)%s\n",
				ColorBlue, ColorReset,
				ColorYellow+ColorBold,
				ColorCyan, msg.FunctionName, ColorYellow+ColorBold,
				ColorWhite, msg.FunctionArgs, ColorYellow+ColorBold,
				ColorReset)
		}
	}

	fmt.Printf("%s╰────────────────────────────────────────────────────────────────────────%s\n\n", ColorBlue, ColorReset)
}

// PrintStreamHeader prints the opening banner for live LLM streaming.
func PrintStreamHeader(path, model string) {
	if model == "" {
		model = "openai-llm"
	}
	fmt.Printf("\n%s%s╭── 🤖 LLM Stream [%s | %s] ──────────────────────────%s\n",
		ColorGreen, ColorBold, path, model, ColorReset)
	fmt.Printf("%s│%s ", ColorGreen, ColorReset)
}

// PrintStreamToken prints a single streaming content delta token.
func PrintStreamToken(token string) {
	fmt.Print(token)
}

// PrintStreamToolCallHeader prints the tool call opening badge during streaming.
func PrintStreamToolCallHeader(name string) {
	fmt.Printf("\n%s│%s   %s↳ 🛠️  Tool Call: %s%s%s(args: ",
		ColorGreen, ColorReset,
		ColorYellow+ColorBold,
		ColorCyan, name, ColorYellow+ColorBold)
}

// PrintStreamToolCallArg prints an argument fragment for a streaming tool call.
func PrintStreamToolCallArg(arg string) {
	fmt.Print(arg)
}

// PrintStreamFooter prints the closing banner with entropy calculation metrics for live LLM streaming.
func PrintStreamFooter(hasToolCalls bool, entropy float64) {
	if hasToolCalls {
		fmt.Printf("%s)%s", ColorYellow+ColorBold, ColorReset)
	}
	fmt.Printf("\n%s╰── (entropy: %.2f bits/byte) ──────────────────────────────────────────%s\n\n",
		ColorGreen, entropy, ColorReset)
}

// PrintNonStreamingResponse prints the full structured response for non-streaming OpenAI chat completions.
func PrintNonStreamingResponse(path, model, content string, toolCalls []PrintToolCall, entropy float64) {
	if model == "" {
		model = "openai-llm"
	}

	fmt.Printf("\n%s%s╭── 🤖 LLM Response [%s | %s] ────────────────────────%s\n", ColorGreen, ColorBold, path, model, ColorReset)
	if content != "" {
		lines := strings.Split(content, "\n")
		for _, l := range lines {
			fmt.Printf("%s│%s %s\n", ColorGreen, ColorReset, l)
		}
	}

	if len(toolCalls) > 0 {
		for _, tc := range toolCalls {
			fmt.Printf("%s│%s   %s↳ 🛠️  Tool Call: %s%s%s(%s%s%s) [ID: %s]%s\n",
				ColorGreen, ColorReset,
				ColorYellow+ColorBold,
				ColorCyan, tc.Name, ColorYellow+ColorBold,
				ColorWhite, tc.Arguments, ColorYellow+ColorBold,
				tc.ID, ColorReset)
		}
	}
	fmt.Printf("%s╰── (entropy: %.2f bits/byte) ──────────────────────────────────────────%s\n\n",
		ColorGreen, entropy, ColorReset)
}

// formatProgressBar creates an ASCII progress meter for sliding window utilization.
func formatProgressBar(current, max int, width int) string {
	if max <= 0 {
		return ""
	}
	pct := float64(current) / float64(max)
	if pct < 0 {
		pct = 0
	}
	filled := int(pct * float64(width))
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)

	var color string
	switch {
	case pct >= 0.9:
		color = ColorRed
	case pct >= 0.7:
		color = ColorYellow
	default:
		color = ColorGreen
	}

	return fmt.Sprintf(" %s[%s]%s %s%.0f%%%s", color, bar, ColorReset, ColorBold, pct*100, ColorReset)
}

// SlidingWindowDisplayInfo encapsulates metrics for the visual Sliding Window terminal dashboard.
type SlidingWindowDisplayInfo struct {
	Path           string
	WindowDuration time.Duration
	TotalRequests  int
	MaxRequests    int
	TotalTokens    int
	MaxTokens      int
	ModelCounts    map[string]int
	EnforceLimits  bool
	LimitExceeded  bool
	LimitReason    string
	Entropy        float64
	LastToolName   string
	LastToolArgs   string
}

// PrintSlidingWindowStatus displays a structured, visual terminal dashboard of the current sliding window state,
// including real-time stream Shannon entropy and the active/last executed tool call with its running parameters.
func PrintSlidingWindowStatus(info SlidingWindowDisplayInfo) {
	fmt.Printf("\n%s%s╭── 📊 SLIDING WINDOW STATUS [%s | %s Window] ────────%s\n",
		ColorCyan, ColorBold, info.Path, info.WindowDuration, ColorReset)

	// Requests Meter
	if info.MaxRequests > 0 {
		reqBar := formatProgressBar(info.TotalRequests, info.MaxRequests, 16)
		fmt.Printf("%s│%s • Total Requests : %s%d%s / %d%s\n", ColorCyan, ColorReset, ColorBold, info.TotalRequests, ColorReset, info.MaxRequests, reqBar)
	} else {
		fmt.Printf("%s│%s • Total Requests : %s%d%s (unlimited)\n", ColorCyan, ColorReset, ColorBold, info.TotalRequests, ColorReset)
	}

	// Tokens Meter
	if info.MaxTokens > 0 {
		tokBar := formatProgressBar(info.TotalTokens, info.MaxTokens, 16)
		fmt.Printf("%s│%s • Total Tokens   : ~%s%d%s / %d%s\n", ColorCyan, ColorReset, ColorBold, info.TotalTokens, ColorReset, info.MaxTokens, tokBar)
	} else {
		fmt.Printf("%s│%s • Total Tokens   : ~%s%d%s tokens (unlimited)\n", ColorCyan, ColorReset, ColorBold, info.TotalTokens, ColorReset)
	}

	// Model Breakdown
	if len(info.ModelCounts) > 0 {
		var models []string
		for m, count := range info.ModelCounts {
			models = append(models, fmt.Sprintf("%s (%d)", m, count))
		}
		sort.Strings(models)
		fmt.Printf("%s│%s • Model Breakdown: %s\n", ColorCyan, ColorReset, strings.Join(models, ", "))
	}

	// Stream Shannon Entropy
	if info.Entropy > 0 {
		var entropyQuality string
		switch {
		case info.Entropy < 2.0:
			entropyQuality = fmt.Sprintf("%s[Low Diversity / Degeneration Risk]%s", ColorRed, ColorReset)
		case info.Entropy < 3.5:
			entropyQuality = fmt.Sprintf("%s[Moderate Diversity]%s", ColorYellow, ColorReset)
		default:
			entropyQuality = fmt.Sprintf("%s[Optimal / High Diversity]%s", ColorGreen, ColorReset)
		}
		fmt.Printf("%s│%s • Stream Entropy : %s%.2f bits/byte%s %s\n", ColorCyan, ColorReset, ColorBold, info.Entropy, ColorReset, entropyQuality)
	}

	// Last / Active Tool Call & Parameters
	if info.LastToolName != "" {
		argsSnippet := strings.TrimSpace(info.LastToolArgs)
		if len(argsSnippet) > 90 {
			argsSnippet = argsSnippet[:87] + "..."
		}
		if argsSnippet == "" {
			argsSnippet = "{}"
		}
		fmt.Printf("%s│%s • Active Tool    : %s🛠️  %s%s%s(args: %s%s%s)%s\n",
			ColorCyan, ColorReset,
			ColorYellow+ColorBold,
			ColorCyan, info.LastToolName, ColorYellow+ColorBold,
			ColorWhite, argsSnippet, ColorYellow+ColorBold,
			ColorReset,
		)
	}

	// Limit Enforcement Status
	if info.LimitExceeded {
		fmt.Printf("%s│%s • Limit Status   : %s%sBREACHED (%s)%s\n", ColorCyan, ColorReset, ColorRed, ColorBold, info.LimitReason, ColorReset)
	} else if info.EnforceLimits {
		fmt.Printf("%s│%s • Limit Status   : %sOK (Enforcing Active)%s\n", ColorCyan, ColorReset, ColorGreen, ColorReset)
	} else {
		fmt.Printf("%s│%s • Limit Status   : %sOK (Monitoring Only)%s\n", ColorCyan, ColorReset, ColorDim, ColorReset)
	}

	fmt.Printf("%s╰────────────────────────────────────────────────────────────────────────%s\n\n", ColorCyan, ColorReset)
}
