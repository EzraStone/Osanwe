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
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "accepted alpha (alpha.example:8443)") {
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
