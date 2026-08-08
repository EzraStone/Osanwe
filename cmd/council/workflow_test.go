package main

import (
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

func TestCouncilFileWorkflowProducesClientValidQuorum(t *testing.T) {
	tmp := t.TempDir()
	now := time.Now().UTC().Truncate(30 * time.Minute).Add(5 * time.Minute)
	run := func(args []string) error { return runArgsAt(args, func() time.Time { return now }) }
	descDir := filepath.Join(tmp, "descriptors")
	if err := os.Mkdir(descDir, 0o755); err != nil {
		t.Fatal(err)
	}
	d := councilTestRelay(t, "alpha", now)
	if err := os.WriteFile(filepath.Join(descDir, "alpha.desc"), d.Encoded(), 0o644); err != nil {
		t.Fatal(err)
	}

	a := councilTestIdentity(t, filepath.Join(tmp, "a.key"))
	b := councilTestIdentity(t, filepath.Join(tmp, "b.key"))
	aPart := filepath.Join(tmp, "a.consensus")
	bPart := filepath.Join(tmp, "b.consensus")
	coSigned := filepath.Join(tmp, "cosigned.consensus")
	final := filepath.Join(tmp, "consensus")
	common := []string{"-descriptors", descDir, "-epoch", "30m", "-lifetime", "3h"}

	if err := run(append([]string{"build", "-identity", filepath.Join(tmp, "a.key"), "-out", aPart}, common...)); err != nil {
		t.Fatalf("build a: %v", err)
	}
	if err := run(append([]string{"build", "-identity", filepath.Join(tmp, "b.key"), "-out", bPart}, common...)); err != nil {
		t.Fatalf("build b: %v", err)
	}

	trustFlags := []string{"-authority", a.Fingerprint(), "-authority", b.Fingerprint()}
	cosignArgs := []string{"cosign", "-identity", filepath.Join(tmp, "b.key"), "-in", aPart, "-out", coSigned}
	cosignArgs = append(cosignArgs, common...)
	cosignArgs = append(cosignArgs, trustFlags...)
	if err := run(cosignArgs); err != nil {
		t.Fatalf("cosign: %v", err)
	}

	authorities, err := directory.AuthoritySet([]string{a.Fingerprint(), b.Fingerprint()})
	if err != nil {
		t.Fatal(err)
	}
	coSignedData, err := os.ReadFile(coSigned)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := directory.ParseConsensus(coSignedData, authorities, 2, now); err != nil {
		t.Fatalf("co-signed output did not meet threshold: %v", err)
	}

	aggregateArgs := []string{"aggregate", "-part", aPart, "-part", bPart, "-out", final,
		"-threshold", "2", "-epoch", "30m", "-lifetime", "3h"}
	aggregateArgs = append(aggregateArgs, trustFlags...)
	if err := run(aggregateArgs); err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	finalData, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	c, err := directory.ParseConsensus(finalData, authorities, 2, now)
	if err != nil {
		t.Fatalf("final consensus was not client-valid: %v", err)
	}
	if len(c.Relays) != 1 || len(c.Signatures) != 2 {
		t.Fatalf("final relays/signatures = %d/%d, want 1/2", len(c.Relays), len(c.Signatures))
	}
}

func TestCouncilBuildersUseEpochWindowForDescriptorFreshness(t *testing.T) {
	tmp := t.TempDir()
	descDir := filepath.Join(tmp, "descriptors")
	if err := os.Mkdir(descDir, 0o755); err != nil {
		t.Fatal(err)
	}
	epoch := 30 * time.Minute
	start := time.Now().UTC().Truncate(epoch)
	id, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	d := councilTestRelay(t, "short-lived", start)
	d.Identity = id.Fingerprint()
	d.Published = start.Add(-time.Hour)
	d.Expires = start.Add(10 * time.Minute) // expires inside the 3-hour consensus window
	if _, err := d.Sign(id); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(descDir, "short.desc"), d.Encoded(), 0o644); err != nil {
		t.Fatal(err)
	}
	councilTestIdentity(t, filepath.Join(tmp, "a.key"))
	councilTestIdentity(t, filepath.Join(tmp, "b.key"))
	aPath, bPath := filepath.Join(tmp, "a.consensus"), filepath.Join(tmp, "b.consensus")
	args := func(key, out string) []string {
		return []string{"build", "-identity", key, "-descriptors", descDir, "-out", out,
			"-epoch", "30m", "-lifetime", "3h"}
	}
	if err := runArgsAt(args(filepath.Join(tmp, "a.key"), aPath), func() time.Time { return start.Add(5 * time.Minute) }); err != nil {
		t.Fatal(err)
	}
	if err := runArgsAt(args(filepath.Join(tmp, "b.key"), bPath), func() time.Time { return start.Add(15 * time.Minute) }); err != nil {
		t.Fatal(err)
	}
	read := func(path string) *directory.Consensus {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		c, err := directory.ParseConsensusPartial(data, start.Add(15*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	first, second := read(aPath), read(bPath)
	if string(first.Raw()) != string(second.Raw()) {
		t.Fatal("builders on opposite sides of descriptor expiry produced different epoch bodies")
	}
	if len(first.Relays) != 0 {
		t.Fatalf("short-lived descriptor was included in a longer consensus window: %d relays", len(first.Relays))
	}
}

func TestCouncilCosignDetectsLocalDescriptorConflict(t *testing.T) {
	tmp := t.TempDir()
	now := time.Now().UTC().Truncate(30 * time.Minute).Add(5 * time.Minute)
	run := func(args []string) error { return runArgsAt(args, func() time.Time { return now }) }
	fullDir := filepath.Join(tmp, "full")
	omittingDir := filepath.Join(tmp, "omitting")
	for _, dir := range []string{fullDir, omittingDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, nickname := range []string{"alpha", "beta"} {
		d := councilTestRelay(t, nickname, now)
		if err := os.WriteFile(filepath.Join(fullDir, nickname+".desc"), d.Encoded(), 0o644); err != nil {
			t.Fatal(err)
		}
		if nickname == "alpha" {
			if err := os.WriteFile(filepath.Join(omittingDir, nickname+".desc"), d.Encoded(), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	a := councilTestIdentity(t, filepath.Join(tmp, "a.key"))
	b := councilTestIdentity(t, filepath.Join(tmp, "b.key"))
	partial := filepath.Join(tmp, "partial")
	if err := run([]string{"build", "-identity", filepath.Join(tmp, "a.key"), "-descriptors", fullDir,
		"-out", partial, "-epoch", "30m", "-lifetime", "3h"}); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"cosign", "-identity", filepath.Join(tmp, "b.key"), "-descriptors", omittingDir,
		"-in", partial, "-out", filepath.Join(tmp, "bad"), "-epoch", "30m", "-lifetime", "3h",
		"-authority", a.Fingerprint(), "-authority", b.Fingerprint()})
	if err == nil {
		t.Fatal("co-signed a body that disagreed with the local descriptor directory")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmp, "bad")); !os.IsNotExist(statErr) {
		t.Fatalf("conflicting output was created: %v", statErr)
	}
}

func TestCouncilOfflineSignerFailsOnMalformedLocalDescriptor(t *testing.T) {
	tmp := t.TempDir()
	now := time.Now().UTC().Truncate(30 * time.Minute).Add(5 * time.Minute)
	descDir := filepath.Join(tmp, "descriptors")
	if err := os.Mkdir(descDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(descDir, "broken.desc"), []byte("not a signed descriptor\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	councilTestIdentity(t, filepath.Join(tmp, "authority.key"))
	out := filepath.Join(tmp, "partial")
	err := runArgsAt([]string{"build", "-identity", filepath.Join(tmp, "authority.key"),
		"-descriptors", descDir, "-out", out, "-epoch", "30m", "-lifetime", "3h"},
		func() time.Time { return now })
	if err == nil {
		t.Fatal("offline signer silently omitted a malformed local descriptor")
	}
	if !strings.Contains(err.Error(), "invalid descriptor") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("signer created output despite local ambiguity: %v", statErr)
	}
}

func TestConsensusFilePublisherRejectsRollbackAndSameEpochConflict(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "consensus")
	ids := []*directory.Identity{
		councilTestIdentity(t, filepath.Join(tmp, "a.key")),
		councilTestIdentity(t, filepath.Join(tmp, "b.key")),
	}
	authorities, err := directory.AuthoritySet([]string{ids[0].Fingerprint(), ids[1].Fingerprint()})
	if err != nil {
		t.Fatal(err)
	}
	epoch := 30 * time.Minute
	lifetime := 3 * time.Hour
	now := time.Now().UTC().Truncate(epoch).Add(10 * time.Minute)
	alpha := councilTestRelay(t, "alpha", now)
	beta := councilTestRelay(t, "beta", now)

	makeFinal := func(at time.Time, relays []*directory.Descriptor) []byte {
		t.Helper()
		parts := make([][]byte, len(ids))
		for i, id := range ids {
			var err error
			parts[i], err = directory.BuildConsensusPartial(relays, id, at, epoch, lifetime)
			if err != nil {
				t.Fatal(err)
			}
		}
		merged, err := directory.MergeConsensus(parts, authorities, 2, now)
		if err != nil {
			t.Fatal(err)
		}
		return merged
	}

	current := makeFinal(now, []*directory.Descriptor{alpha})
	if err := os.WriteFile(path, current, 0o644); err != nil {
		t.Fatal(err)
	}
	clock := now
	pub := &consensusFilePublisher{
		path:         path,
		servingState: filepath.Join(tmp, "serve-state"),
		authorities:  authorities,
		threshold:    2,
		epoch:        epoch,
		lifetime:     lifetime,
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:          func() time.Time { return clock },
	}
	if err := pub.reload(); err != nil {
		t.Fatalf("initial reload: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/consensus", nil)
	rec := httptest.NewRecorder()
	pub.serve(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != string(current) {
		t.Fatalf("serve = HTTP %d body match %v", rec.Code, rec.Body.String() == string(current))
	}

	rollback := makeFinal(now.Add(-epoch), []*directory.Descriptor{alpha})
	if err := os.WriteFile(path, rollback, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pub.reload(); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("rollback reload error = %v", err)
	}
	// A new process with no in-memory history must reach the same decision from
	// the persistent serving-state file.
	restarted := &consensusFilePublisher{
		path:         path,
		servingState: pub.servingState,
		authorities:  authorities,
		threshold:    2,
		epoch:        epoch,
		lifetime:     lifetime,
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:          func() time.Time { return clock },
	}
	if err := restarted.reload(); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("restart forgot served rollback state: %v", err)
	}

	conflict := makeFinal(now, []*directory.Descriptor{beta})
	if err := os.WriteFile(path, conflict, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pub.reload(); err == nil || !strings.Contains(err.Error(), "conflicting body") {
		t.Fatalf("same-epoch conflict reload error = %v", err)
	}

	clock = pub.parsed.ValidUntil
	rec = httptest.NewRecorder()
	pub.serve(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expired consensus served with HTTP %d", rec.Code)
	}
}

func TestAuthorityPublisherDoesNotEquivocateWithinEpoch(t *testing.T) {
	tmp := t.TempDir()
	descDir := filepath.Join(tmp, "descriptors")
	if err := os.Mkdir(descDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(30 * time.Minute).Add(10 * time.Minute)
	alpha := councilTestRelay(t, "alpha", now)
	if err := os.WriteFile(filepath.Join(descDir, "alpha.desc"), alpha.Encoded(), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	p := &publisher{
		dir:          descDir,
		id:           id,
		signingState: filepath.Join(tmp, "signing-state"),
		epoch:        30 * time.Minute,
		lifetime:     3 * time.Hour,
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:          func() time.Time { return clock },
	}
	if err := p.rebuild(); err != nil {
		t.Fatalf("initial rebuild: %v", err)
	}
	first := append([]byte(nil), p.current...)
	if err := p.rebuild(); err != nil {
		t.Fatalf("idempotent same-epoch rebuild: %v", err)
	}
	if string(p.current) != string(first) {
		t.Fatal("an unchanged same-epoch rebuild changed the partial")
	}

	beta := councilTestRelay(t, "beta", now)
	if err := os.WriteFile(filepath.Join(descDir, "beta.desc"), beta.Encoded(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.rebuild(); err == nil || !strings.Contains(err.Error(), "conflicting body") {
		t.Fatalf("same-epoch rebuild error = %v", err)
	}
	if string(p.current) != string(first) {
		t.Fatal("publisher replaced the first body after a same-epoch conflict")
	}

	clock = clock.Add(30 * time.Minute)
	if err := p.rebuild(); err != nil {
		t.Fatalf("next-epoch rebuild: %v", err)
	}
	if string(p.current) == string(first) {
		t.Fatal("publisher did not admit the descriptor change in the next epoch")
	}
}

func councilTestIdentity(t *testing.T, path string) *directory.Identity {
	t.Helper()
	id, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.WriteIdentity(id, path); err != nil {
		t.Fatal(err)
	}
	return id
}

func councilTestRelay(t *testing.T, nickname string, now time.Time) *directory.Descriptor {
	t.Helper()
	id, err := directory.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	d := &directory.Descriptor{
		Nickname:     nickname,
		Address:      nickname + ".example:8443",
		TLSPin:       "sha256/" + strings.Repeat("A", 42) + "8=",
		Identity:     id.Fingerprint(),
		Destinations: []string{"api.anthropic.com:443"},
		Published:    now.Add(-time.Hour),
		Expires:      now.Add(24 * time.Hour),
	}
	if _, err := d.Sign(id); err != nil {
		t.Fatal(err)
	}
	return d
}
