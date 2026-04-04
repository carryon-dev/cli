package cmd

import (
	"fmt"
	"time"

	"github.com/fatih/color"
)

var (
	styleID     = color.New(color.FgHiBlack).SprintFunc()
	styleAccent = color.New(color.FgCyan).SprintFunc()
	styleDim    = color.New(color.FgHiBlack).SprintFunc()
	styleHint   = color.New(color.FgHiBlack).SprintFunc()
	styleError  = color.New(color.FgRed).SprintFunc()
	styleWarn   = color.New(color.FgYellow).SprintFunc()
)

// relativeTime converts a Unix millisecond timestamp to a human-friendly
// relative time string. Falls back to an absolute date after ~2 weeks.
func relativeTime(createdMs int64) string {
	created := time.UnixMilli(createdMs)
	d := time.Since(created)

	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 8*7*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	default:
		return created.Format("2006-01-02")
	}
}

// clientsText returns a styled string for the client count.
func clientsText(n int) string {
	switch {
	case n == 0:
		return styleDim("no clients")
	case n == 1:
		return "1 client"
	default:
		return fmt.Sprintf("%d clients", n)
	}
}
