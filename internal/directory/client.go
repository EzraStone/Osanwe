package directory

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"time"
)

// MaxConsensusSize bounds what a client will read from a directory. Without a
// limit, a hostile or broken authority could exhaust a client's memory simply
// by never stopping.
const MaxConsensusSize = 8 << 20 // 8 MiB

// Fetcher retrieves and verifies a consensus.
type Fetcher struct {
	// URLs are directory endpoints. They are tried in random order, so a
	// client does not always lean on the same authority and one slow endpoint
	// does not become everybody's first hop.
	URLs []string

	// Authorities are the trusted signing keys, and Threshold is how many must
	// have signed. Both are required: a Fetcher with no authorities would
	// accept anything.
	Authorities map[string]ed25519.PublicKey
	Threshold   int

	// HTTPClient is optional.
	HTTPClient *http.Client

	// Now is overridable for tests.
	Now func() time.Time
}

// Fetch retrieves a consensus from the first endpoint that yields a valid one.
//
// Verification happens before anything is returned, so a caller cannot
// accidentally use an unverified document. Transport security is not relied on
// at all: the consensus is signed, so fetching it over plain HTTP from an
// untrusted mirror is fine, and fetching it over HTTPS from a hostile
// authority still would not help.
func (f *Fetcher) Fetch(ctx context.Context) (*Consensus, error) {
	if len(f.URLs) == 0 {
		return nil, fmt.Errorf("directory: no directory URLs configured")
	}
	if len(f.Authorities) == 0 {
		return nil, fmt.Errorf("directory: no authority keys configured; a fetcher without them would accept any document")
	}
	if f.Threshold < 1 {
		return nil, fmt.Errorf("directory: threshold must be at least 1")
	}

	client := f.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	now := f.Now
	if now == nil {
		now = time.Now
	}

	order := rand.Perm(len(f.URLs))
	var errs []error

	for _, i := range order {
		url := f.URLs[i]
		c, err := f.fetchOne(ctx, client, url, now())
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", url, err))
			continue
		}
		return c, nil
	}
	return nil, fmt.Errorf("directory: no endpoint returned a valid consensus: %v", errs)
}

func (f *Fetcher) fetchOne(ctx context.Context, client *http.Client, url string, now time.Time) (*Consensus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "osanwe-bearer")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// LimitReader is one byte over the maximum so an exactly-oversized body is
	// detected rather than silently truncated into a parse error.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxConsensusSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxConsensusSize {
		return nil, fmt.Errorf("consensus larger than %d bytes", MaxConsensusSize)
	}

	return ParseConsensus(body, f.Authorities, f.Threshold, now)
}

// Select picks a relay for a destination.
//
// Choice is random among the relays that can serve it. Always preferring the
// first entry would concentrate every client on one relay, which is bad for
// load and worse for privacy: a relay carrying everybody's traffic sees
// everybody's timing.
func Select(candidates []*Descriptor) (*Descriptor, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("directory: no relay in the consensus serves that destination")
	}
	return candidates[rand.IntN(len(candidates))], nil
}
