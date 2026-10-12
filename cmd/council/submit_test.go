package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EzraStone/osanwe/internal/directory"
)

type submitFixture struct {
	t       *testing.T
	store   *directory.Store
	pub     *publisher
	handler http.HandlerFunc
	accept  string
	now     time.Time
}

func newSubmitFixture(t *testing.T, admitted ...*directory.Identity) *submitFixture {
	t.Helper()
	tmp := t.TempDir()
	descDir := filepath.Join(tmp, "descriptors")
	if err := os.Mkdir(descDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The real time, not a convenient one: the handler's store checks
	// freshness against the wall clock, so a fixture clock ahead of it makes
	// a descriptor "published a minute ago" look like one from the future.
	f := &submitFixture{t: t, accept: filepath.Join(tmp, "accept"), now: time.Now().UTC()}
	f.writeAccept(admitted...)
	accept, err := directory.NewAcceptList(f.accept)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.store = &directory.Store{Dir: descDir, Accept: accept}
	f.pub = &publisher{
		dir: descDir, id: authority, signingState: filepath.Join(tmp, "signing-state"),
		epoch: 30 * time.Minute, lifetime: 3 * time.Hour, log: log,
		now: func() time.Time { return f.now },
	}
	f.handler = submitHandler(f.store, f.pub, log)
	return f
}

func (f *submitFixture) writeAccept(ids ...*directory.Identity) {
	f.t.Helper()
	body := "# relays admitted to this authority\n"
	for _, id := range ids {
		body += id.Fingerprint() + " test-operator\n"
	}
	if err := os.WriteFile(f.accept, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *submitFixture) descriptor(id *directory.Identity, nickname string, published time.Time) []byte {
	f.t.Helper()
	d := &directory.Descriptor{
		Nickname:     nickname,
		Address:      nickname + ".example:8443",
		TLSPin:       "sha256/" + strings.Repeat("A", 42) + "8=",
		Identity:     id.Fingerprint(),
		Destinations: []string{"api.anthropic.com:443"},
		Published:    published,
		Expires:      published.Add(24 * time.Hour),
	}
	encoded, err := d.Sign(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return encoded
}

func (f *submitFixture) post(body []byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.handler(rec, httptest.NewRequest(http.MethodPost, "/publish", bytes.NewReader(body)))
	return rec
}

func newRelayIdentity(t *testing.T) *directory.Identity {
	t.Helper()
	id, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSubmitOnlyAcceptsPOST(t *testing.T) {
	f := newSubmitFixture(t)
	rec := httptest.NewRecorder()
	f.handler(rec, httptest.NewRequest(http.MethodGet, "/publish", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET: %d, Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestSubmitWithoutAnAcceptListRefusesEverything(t *testing.T) {
	f := newSubmitFixture(t)
	id := newRelayIdentity(t)
	rec := httptest.NewRecorder()
	submitHandler(nil, f.pub, slog.New(slog.NewTextHandler(io.Discard, nil)))(rec,
		httptest.NewRequest(http.MethodPost, "/publish", bytes.NewReader(f.descriptor(id, "alpha", f.now.Add(-time.Minute)))))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("submission to an authority with no accept list: %d", rec.Code)
	}
}

func TestSubmitRejectsWhatItCannotVerify(t *testing.T) {
	id := newRelayIdentity(t)
	f := newSubmitFixture(t, id)
	signed := f.descriptor(id, "alpha", f.now.Add(-time.Minute))
	tampered := bytes.Replace(signed, []byte("alpha.example"), []byte("evil0.example"), 1)

	for name, test := range map[string]struct {
		body []byte
		want int
	}{
		"oversized":             {bytes.Repeat([]byte("x"), maxSubmission+1), http.StatusRequestEntityTooLarge},
		"not a descriptor":      {[]byte("hello authority\n"), http.StatusBadRequest},
		"altered after signing": {tampered, http.StatusBadRequest},
	} {
		if rec := f.post(test.body); rec.Code != test.want {
			t.Errorf("%s: %d %q, want %d", name, rec.Code, rec.Body.String(), test.want)
		}
	}
	if _, err := os.Stat(f.store.FileFor(id.Fingerprint())); err == nil {
		t.Fatal("a rejected submission was stored")
	}
}

func TestSubmitRefusesARelayThatIsNotAdmitted(t *testing.T) {
	admitted, stranger := newRelayIdentity(t), newRelayIdentity(t)
	f := newSubmitFixture(t, admitted)
	rec := f.post(f.descriptor(stranger, "mallory", f.now.Add(-time.Minute)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unadmitted relay: %d %q", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(f.store.FileFor(stranger.Fingerprint())); err == nil {
		t.Fatal("an unadmitted relay's descriptor was stored")
	}
}

func TestSubmitAcceptsAnAdmittedRelayAndPublishesIt(t *testing.T) {
	id := newRelayIdentity(t)
	f := newSubmitFixture(t, id)
	// Published before this epoch began, so it is valid for the whole window
	// and the rebuild that follows acceptance can list it straight away.
	rec := f.post(f.descriptor(id, "alpha", f.now.Truncate(f.pub.epoch).Add(-time.Minute)))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "accepted alpha (alpha.example:8443); eligible for the current consensus") {
		t.Fatalf("admitted relay: %d %q", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(f.store.FileFor(id.Fingerprint())); err != nil {
		t.Fatalf("accepted descriptor was not stored: %v", err)
	}
	f.pub.mu.RLock()
	count, current := f.pub.count, f.pub.current
	f.pub.mu.RUnlock()
	partial, err := directory.ParseConsensusPartial(current, f.now)
	if err != nil {
		t.Fatalf("rebuilt partial does not parse: %v", err)
	}
	if count != 1 || len(partial.Relays) != 1 || partial.Relays[0].Identity != id.Fingerprint() {
		t.Fatalf("accepted relay is not in the rebuilt consensus (count %d, relays %d)", count, len(partial.Relays))
	}
}

func TestSubmitWillNotRollARelayBack(t *testing.T) {
	id := newRelayIdentity(t)
	f := newSubmitFixture(t, id)
	if rec := f.post(f.descriptor(id, "alpha", f.now.Add(-time.Minute))); rec.Code != http.StatusAccepted {
		t.Fatalf("first submission: %d %q", rec.Code, rec.Body.String())
	}
	if rec := f.post(f.descriptor(id, "alpha", f.now.Add(-time.Hour))); rec.Code != http.StatusConflict {
		t.Fatalf("older submission: %d %q, want 409", rec.Code, rec.Body.String())
	}
}

func TestSubmitSeesAcceptListChangesWithoutARestart(t *testing.T) {
	id := newRelayIdentity(t)
	f := newSubmitFixture(t)
	body := f.descriptor(id, "alpha", f.now.Add(-time.Minute))
	if rec := f.post(body); rec.Code != http.StatusForbidden {
		t.Fatalf("before admission: %d", rec.Code)
	}
	f.writeAccept(id)
	if rec := f.post(body); rec.Code != http.StatusAccepted {
		t.Fatalf("after admission: %d %q", rec.Code, rec.Body.String())
	}
}

func (f *submitFixture) descriptorValidUntil(id *directory.Identity, nickname string, published, expires time.Time) []byte {
	f.t.Helper()
	d := &directory.Descriptor{
		Nickname:     nickname,
		Address:      nickname + ".example:8443",
		TLSPin:       "sha256/" + strings.Repeat("A", 42) + "8=",
		Identity:     id.Fingerprint(),
		Destinations: []string{"api.anthropic.com:443"},
		Published:    published,
		Expires:      expires,
	}
	encoded, err := d.Sign(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return encoded
}

// A descriptor that expires before any consensus window could end was once
// accepted with 202 and then never listed, so its operator waited forever.
func TestSubmitRefusesADescriptorNoConsensusCouldList(t *testing.T) {
	id := newRelayIdentity(t)
	f := newSubmitFixture(t, id)
	rec := f.post(f.descriptorValidUntil(id, "brief", f.now.Add(-time.Minute), f.now.Add(59*time.Minute)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("short-lived descriptor: %d %q, want 422", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "never listed") || !strings.Contains(body, "-descriptor-validity of at least 3h30m0s") {
		t.Errorf("refusal does not say what to change: %q", body)
	}
	if _, err := os.Stat(f.store.FileFor(id.Fingerprint())); err == nil {
		t.Fatal("an unlistable descriptor was stored")
	}
}

func TestSubmitAcceptsADescriptorTheNextConsensusCanList(t *testing.T) {
	id := newRelayIdentity(t)
	f := newSubmitFixture(t, id)
	next := f.now.Truncate(f.pub.epoch).Add(f.pub.epoch)
	rec := f.post(f.descriptorValidUntil(id, "just", f.now.Add(-time.Minute), next.Add(f.pub.lifetime)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("descriptor valid for exactly the next window: %d %q", rec.Code, rec.Body.String())
	}
}

func TestSubmitTellsAnUnadmittedRelayAboutAdmissionFirst(t *testing.T) {
	f := newSubmitFixture(t)
	stranger := newRelayIdentity(t)
	rec := f.post(f.descriptorValidUntil(stranger, "brief", f.now.Add(-time.Minute), f.now.Add(59*time.Minute)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unadmitted short-lived descriptor: %d, want 403", rec.Code)
	}
}

// firstListing is checked against the publisher's own clock, so unlike the
// handler tests above it can be placed anywhere in an epoch deterministically.
func TestFirstListingFindsTheEarliestWindowThatCanHoldADescriptor(t *testing.T) {
	id := newRelayIdentity(t)
	f := newSubmitFixture(t, id)
	start := f.now.Truncate(f.pub.epoch)
	f.now = start.Add(10 * time.Minute) // well past the publication skew
	next := start.Add(f.pub.epoch)

	parse := func(published, expires time.Time) *directory.Descriptor {
		d, err := directory.ParseDescriptor(f.descriptorValidUntil(id, "alpha", published, expires))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	tests := []struct {
		name      string
		published time.Time
		expires   time.Time
		want      time.Time
		ok        bool
	}{
		{"published before this window", start.Add(-time.Minute), start.Add(24 * time.Hour), start, true},
		{"published mid-window, long-lived", f.now.Add(-time.Minute), f.now.Add(24 * time.Hour), next, true},
		{"published mid-window, just long enough", f.now.Add(-time.Minute), next.Add(f.pub.lifetime), next, true},
		{"published mid-window, one second short", f.now.Add(-time.Minute), next.Add(f.pub.lifetime - time.Second), time.Time{}, false},
		{"far too short", f.now.Add(-time.Minute), f.now.Add(59 * time.Minute), time.Time{}, false},
	}
	for _, test := range tests {
		got, ok := f.pub.firstListing(parse(test.published, test.expires))
		if ok != test.ok || !got.Equal(test.want) {
			t.Errorf("%s: firstListing = %s, %v; want %s, %v", test.name, got, ok, test.want, test.ok)
		}
	}
}

// Once an epoch's body is signed, a newly admitted relay waits for the next
// epoch, and the authority must say so rather than let the operator find the
// relay missing from the consensus they fetch next.
func TestSubmitSaysWhenAQueuedRelayBecomesEligible(t *testing.T) {
	alpha, beta := newRelayIdentity(t), newRelayIdentity(t)
	f := newSubmitFixture(t, alpha, beta)
	start := f.now.Truncate(f.pub.epoch)

	first := f.post(f.descriptor(alpha, "alpha", start.Add(-time.Minute)))
	if !strings.Contains(first.Body.String(), "eligible for the current consensus") {
		t.Fatalf("first relay: %d %q", first.Code, first.Body.String())
	}
	second := f.post(f.descriptor(beta, "beta", start.Add(-time.Minute)))
	want := "eligible from the consensus beginning " + start.Add(f.pub.epoch).UTC().Format(time.RFC3339)
	if second.Code != http.StatusAccepted || !strings.Contains(second.Body.String(), want) {
		t.Fatalf("relay queued behind a signed epoch: %d %q, want %q", second.Code, second.Body.String(), want)
	}
}
