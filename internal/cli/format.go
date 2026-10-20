package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

var (
	colorReset  = "\033[0m"
	colorBold   = "\033[1m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorRed    = "\033[31m"
	colorMuted  = "\033[90m"
)

func init() {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		colorReset = ""
		colorBold = ""
		colorGreen = ""
		colorYellow = ""
		colorBlue = ""
		colorCyan = ""
		colorRed = ""
		colorMuted = ""
	}
}

// FormatBytes formats byte counts into human-readable strings (e.g. 1.2 MB).
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// FormatTimeRelative returns a short relative time string (e.g. "2m ago", "just now").
func FormatTimeRelative(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	diff := time.Since(t)
	if diff < 5*time.Second {
		return "just now"
	}
	if diff < time.Minute {
		return fmt.Sprintf("%ds ago", int(diff.Seconds()))
	}
	if diff < time.Hour {
		return fmt.Sprintf("%dm ago", int(diff.Minutes()))
	}
	if diff < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(diff.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(diff.Hours()/24))
}

// NewTableWriter creates a configured tabwriter.Writer.
func NewTableWriter(out io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
}

// PrintHeader prints a section header in bold cyan.
func PrintHeader(w io.Writer, title string) {
	fmt.Fprintf(w, "%s%s=== %s ===%s\n", colorBold, colorCyan, title, colorReset)
}

// StatusDot returns a colorized dot and text for a status.
func StatusDot(online bool, text string) string {
	if online {
		return colorGreen + "● " + text + colorReset
	}
	return colorMuted + "○ " + text + colorReset
}

// Colorize returns the text wrapped in the given color if colors are active.
func Colorize(color, text string) string {
	return color + text + colorReset
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func cleanString(s string) string {
	return strings.TrimSpace(s)
}
