package gateway

import (
	"strings"
	"testing"
	"time"
)

func TestRouteAnnotationsCarrySourcedPolicyAndExpiry(t *testing.T) {
	policy, lifecycle, err := parseRouteAnnotations(strings.Fields(
		"retention=retained training=unknown provider_identity=undisclosed " +
			"policy_source=https://openrouter.ai/stealth/ox-alpha policy_checked=2026-08-22 " +
			"experimental=true expires=2026-08-29T00:00:00Z"))
	if err != nil {
		t.Fatalf("parseRouteAnnotations: %v", err)
	}
	if policy.Retention != ProviderRetentionRetained || policy.Training != ProviderTrainingUnknown ||
		policy.Identity != ProviderIdentityUndisclosed {
		t.Fatalf("policy = %+v", policy)
	}
	if got := policy.CheckedAt.Format("2006-01-02"); got != "2026-08-22" {
		t.Fatalf("checked = %s", got)
	}
	if !lifecycle.Experimental || lifecycle.ExpiresAt.Format(time.RFC3339) != "2026-08-29T00:00:00Z" {
		t.Fatalf("lifecycle = %+v", lifecycle)
	}
	if !lifecycle.active(time.Date(2026, 8, 28, 23, 59, 59, 0, time.UTC)) {
		t.Fatal("route was inactive before its expiry")
	}
	if lifecycle.active(lifecycle.ExpiresAt) {
		t.Fatal("route remained active at its exact expiry")
	}
}

func TestRoutePolicyDefaultsStayUnknown(t *testing.T) {
	policy, lifecycle, err := parseRouteAnnotations(nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Retention != ProviderRetentionUnknown || policy.Training != ProviderTrainingUnknown ||
		policy.Identity != ProviderIdentityUnknown {
		t.Fatalf("defaults = %+v", policy)
	}
	if lifecycle.Experimental || !lifecycle.ExpiresAt.IsZero() {
		t.Fatalf("lifecycle defaults = %+v", lifecycle)
	}
}

func TestBadRouteAnnotationsAreRefused(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"unknown key", "privacy=excellent", "unknown annotation"},
		{"duplicate key", "retention=unknown retention=retained", "appears twice"},
		{"bad retention", "retention=forever policy_source=https://x.example/p policy_checked=2026-08-22", "retention must"},
		{"bad training", "training=never policy_source=https://x.example/p policy_checked=2026-08-22", "training must"},
		{"bad identity", "provider_identity=anonymous policy_source=https://x.example/p policy_checked=2026-08-22", "provider_identity must"},
		{"unsourced claim", "retention=none", "requires policy_source"},
		{"source without date", "policy_source=https://x.example/p", "set together"},
		{"date without source", "policy_checked=2026-08-22", "set together"},
		{"plaintext source", "retention=none policy_source=http://x.example/p policy_checked=2026-08-22", "absolute https"},
		{"source query", "retention=none policy_source=https://x.example/p?a=b policy_checked=2026-08-22", "must not contain"},
		{"bad date", "policy_checked=08/22/2026", "YYYY-MM-DD"},
		{"bad bool", "experimental=yes expires=2026-08-29T00:00:00Z", "true or false"},
		{"bad expiry", "experimental=true expires=next-week", "RFC3339"},
		{"missing expiry", "experimental=true", "require expires"},
		{"expiry without experimental", "expires=2026-08-29T00:00:00Z", "requires experimental"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseRouteAnnotations(strings.Fields(tc.text))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}
