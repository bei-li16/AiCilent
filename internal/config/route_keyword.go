package config

import (
	"strconv"
	"strings"
)

// ParsePriorityKeyword parses priority-based routing keywords used both as
// client model names and as model_routes targets:
//
//	"P2"     → priority=2, mode="only"  (P2 only)
//	"P2up"   → priority=2, mode="up"    (P1..P2)
//	"P2down" → priority=2, mode="down"  (P2..lowest)
//
// The leading P and the up/down suffixes are case-insensitive; Max/Flash/
// Medium are handled by the router and not parsed here.
func ParsePriorityKeyword(s string) (priority int, mode string, ok bool) {
	if len(s) < 2 || (s[0] != 'P' && s[0] != 'p') {
		return 0, "", false
	}
	i := 1
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 1 {
		return 0, "", false
	}
	n, err := strconv.Atoi(s[1:i])
	if err != nil || n <= 0 {
		return 0, "", false
	}
	switch strings.ToLower(s[i:]) {
	case "":
		return n, "only", true
	case "up":
		return n, "up", true
	case "down":
		return n, "down", true
	default:
		return 0, "", false
	}
}

// IsRouteKeyword reports whether s is a routing keyword usable as a
// model_routes target: "Max", "Flash", "Medium", or "P{n}[up|down]".
// Case rules match client-sent model names exactly.
func IsRouteKeyword(s string) bool {
	switch s {
	case "Max", "Flash", "Medium":
		return true
	}
	_, _, ok := ParsePriorityKeyword(s)
	return ok
}
