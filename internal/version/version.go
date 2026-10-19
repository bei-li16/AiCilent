package version

import (
	_ "embed"
	"fmt"
	"runtime"
	"strings"
)

//go:embed VERSION
var releaseVersion string

var (
	Version   = ""
	Commit    = "none"
	BuildDate = "unknown"
)

func init() {
	if Version == "" {
		Version = strings.TrimSpace(releaseVersion)
	}
}

func String() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("ai-proxy version %s\n", Version))
	if Commit != "none" {
		b.WriteString(fmt.Sprintf("  commit:    %s\n", Commit))
	}
	if BuildDate != "unknown" {
		b.WriteString(fmt.Sprintf("  built:     %s\n", BuildDate))
	}
	b.WriteString(fmt.Sprintf("  go:        %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH))
	return b.String()
}
