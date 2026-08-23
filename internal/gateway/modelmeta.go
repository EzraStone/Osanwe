package gateway

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ProviderRetention is the provider's documented handling of request and
// response content. It is deliberately coarse: a gateway should publish an
// unknown fact rather than turn a complicated policy into a favorable guess.
type ProviderRetention string

const (
	ProviderRetentionUnknown  ProviderRetention = "unknown"
	ProviderRetentionNone     ProviderRetention = "none"
	ProviderRetentionRetained ProviderRetention = "retained"
)

// ProviderTraining records whether submitted content may be used for model
// training or improvement under the policy source the operator reviewed.
type ProviderTraining string

const (
	ProviderTrainingUnknown   ProviderTraining = "unknown"
	ProviderTrainingNotUsed   ProviderTraining = "not_used"
	ProviderTrainingMayBeUsed ProviderTraining = "may_be_used"
)

// ProviderIdentity describes whether the developer or operator behind the
// model is named. "Undisclosed" is intentionally not called anonymous: that
// word could be mistaken for a claim about the user or request.
type ProviderIdentity string

const (
	ProviderIdentityUnknown     ProviderIdentity = "unknown"
	ProviderIdentityDisclosed   ProviderIdentity = "disclosed"
	ProviderIdentityUndisclosed ProviderIdentity = "undisclosed"
)

// ProviderPolicy is attributable, dated model-policy metadata. Any favorable
// or unfavorable claim must carry both SourceURL and CheckedAt; otherwise the
// gateway refuses to start instead of publishing an unsourced label.
type ProviderPolicy struct {
	Retention ProviderRetention
	Training  ProviderTraining
	Identity  ProviderIdentity
	SourceURL string
	CheckedAt time.Time
}

// ModelLifecycle makes temporary routes fail closed. An experimental route
// must have an expiry and stops being listed or callable at that instant.
type ModelLifecycle struct {
	Experimental bool
	ExpiresAt    time.Time
}

func (p ProviderPolicy) normalized() ProviderPolicy {
	if p.Retention == "" {
		p.Retention = ProviderRetentionUnknown
	}
	if p.Training == "" {
		p.Training = ProviderTrainingUnknown
	}
	if p.Identity == "" {
		p.Identity = ProviderIdentityUnknown
	}
	return p
}

func (p ProviderPolicy) validate() error {
	p = p.normalized()
	if !oneOf(string(p.Retention), string(ProviderRetentionUnknown), string(ProviderRetentionNone), string(ProviderRetentionRetained)) {
		return fmt.Errorf("retention must be unknown, none, or retained, got %q", p.Retention)
	}
	if !oneOf(string(p.Training), string(ProviderTrainingUnknown), string(ProviderTrainingNotUsed), string(ProviderTrainingMayBeUsed)) {
		return fmt.Errorf("training must be unknown, not_used, or may_be_used, got %q", p.Training)
	}
	if !oneOf(string(p.Identity), string(ProviderIdentityUnknown), string(ProviderIdentityDisclosed), string(ProviderIdentityUndisclosed)) {
		return fmt.Errorf("provider_identity must be unknown, disclosed, or undisclosed, got %q", p.Identity)
	}

	hasSource := p.SourceURL != ""
	hasDate := !p.CheckedAt.IsZero()
	if hasSource != hasDate {
		return fmt.Errorf("policy_source and policy_checked must be set together")
	}
	claimed := p.Retention != ProviderRetentionUnknown ||
		p.Training != ProviderTrainingUnknown || p.Identity != ProviderIdentityUnknown
	if claimed && !hasSource {
		return fmt.Errorf("non-unknown provider policy requires policy_source and policy_checked")
	}
	if hasSource {
		u, err := url.Parse(p.SourceURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("policy_source must be an absolute https URL")
		}
		if u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("policy_source must not contain user information, a query, or a fragment")
		}
	}
	return nil
}

func (l ModelLifecycle) validate() error {
	if l.Experimental && l.ExpiresAt.IsZero() {
		return fmt.Errorf("experimental routes require expires")
	}
	if !l.Experimental && !l.ExpiresAt.IsZero() {
		return fmt.Errorf("expires requires experimental=true")
	}
	return nil
}

func (l ModelLifecycle) active(now time.Time) bool {
	return l.ExpiresAt.IsZero() || now.Before(l.ExpiresAt)
}

func parseRouteAnnotations(fields []string) (ProviderPolicy, ModelLifecycle, error) {
	policy := ProviderPolicy{}.normalized()
	var lifecycle ModelLifecycle
	seen := make(map[string]struct{}, len(fields))

	for _, field := range fields {
		name, value, ok := strings.Cut(field, "=")
		if !ok || name == "" || value == "" {
			return ProviderPolicy{}, ModelLifecycle{}, fmt.Errorf("annotation %q must be key=value", field)
		}
		if _, duplicate := seen[name]; duplicate {
			return ProviderPolicy{}, ModelLifecycle{}, fmt.Errorf("annotation %q appears twice", name)
		}
		seen[name] = struct{}{}

		switch name {
		case "retention":
			policy.Retention = ProviderRetention(value)
		case "training":
			policy.Training = ProviderTraining(value)
		case "provider_identity":
			policy.Identity = ProviderIdentity(value)
		case "policy_source":
			policy.SourceURL = value
		case "policy_checked":
			checked, err := time.Parse("2006-01-02", value)
			if err != nil {
				return ProviderPolicy{}, ModelLifecycle{}, fmt.Errorf("policy_checked must be YYYY-MM-DD: %w", err)
			}
			policy.CheckedAt = checked
		case "experimental":
			if value != "true" && value != "false" {
				return ProviderPolicy{}, ModelLifecycle{}, fmt.Errorf("experimental must be true or false")
			}
			lifecycle.Experimental, _ = strconv.ParseBool(value)
		case "expires":
			expires, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return ProviderPolicy{}, ModelLifecycle{}, fmt.Errorf("expires must be RFC3339: %w", err)
			}
			lifecycle.ExpiresAt = expires
		default:
			return ProviderPolicy{}, ModelLifecycle{}, fmt.Errorf("unknown annotation %q", name)
		}
	}
	if err := policy.validate(); err != nil {
		return ProviderPolicy{}, ModelLifecycle{}, err
	}
	if err := lifecycle.validate(); err != nil {
		return ProviderPolicy{}, ModelLifecycle{}, err
	}
	return policy.normalized(), lifecycle, nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
