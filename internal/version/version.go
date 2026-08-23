// Package version holds release metadata injected by the build workflow.
package version

import "fmt"

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String returns one support-friendly line without exposing runtime or host
// details that could become an unnecessary fingerprint.
func String(command string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s)", command, Version, Commit, Date)
}
