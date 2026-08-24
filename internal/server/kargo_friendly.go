package server

// kargo_friendly.go — translation of raw Kargo/CD failure messages into
// human-readable status the UI can show. End users should never need to
// understand Kargo internals: a promotion either ships their release or the
// platform explains, in plain language, what is broken and that it is (in
// almost every case) a platform problem worth reporting — not something wrong
// with their app.

import "strings"

// friendlyKargoIssue maps a raw Kargo condition/error message to a
// human-friendly explanation. The raw message is appended in parentheses so a
// user can paste it into a bug report; unknown messages pass through with a
// generic framing. Empty input returns "".
func friendlyKargoIssue(raw string) string {
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	var friendly string
	switch {
	case strings.Contains(lower, "no current freight"):
		return "No release has flowed through the CD pipeline yet — the pipeline activates with the first release after this deployment."
	case strings.Contains(lower, "argo cd integration is disabled"):
		friendly = "The CD controller cannot update deployments because its Argo CD integration is disabled. This is a platform configuration issue — please report it."
	case strings.Contains(lower, "http response to https client") ||
		strings.Contains(lower, "tls handshake") ||
		strings.Contains(lower, "certificate"):
		friendly = "The image registry could not be reached securely (TLS). This is a platform configuration issue — nothing to change in your app."
	case strings.Contains(lower, "refused to get credentials for insecure http endpoint") ||
		strings.Contains(lower, "could not read username"):
		friendly = "The CD pipeline could not authenticate to the git server. This is a platform configuration issue — please report it."
	case strings.Contains(lower, "discoveryfailure") ||
		strings.Contains(lower, "error discovering"):
		friendly = "The CD pipeline could not discover new images in the registry. This is usually a platform-side registry or credentials issue."
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "forbidden"):
		friendly = "The CD pipeline was denied access by the cluster. This is a platform permissions issue — please report it."
	case strings.Contains(lower, "not found"):
		friendly = "Part of the CD pipeline for this app is still being set up. If this persists for more than a few minutes, please report it."
	default:
		friendly = "The CD pipeline reported an error. This is likely a platform issue — please report it."
	}
	return friendly + " (detail: " + raw + ")"
}
