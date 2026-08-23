package bearer

import (
	"context"
	"net"
	"testing"
)

type configTestDialer struct{}

func (configTestDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, net.ErrClosed
}

func TestEmbeddedChatAPIStyleAndModelFilterAreExplicit(t *testing.T) {
	s, err := New(Config{
		Addr: "127.0.0.1:0", Upstream: "https://openrouter.ai/api",
		APIStyle: APIStyleOpenAI, Models: []string{" stealth/ox-alpha ", "stealth/ox-alpha"},
		Dialer: configTestDialer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	status := s.Status()
	if status.APIStyle != APIStyleOpenAI {
		t.Fatalf("APIStyle = %q", status.APIStyle)
	}
	if len(status.Models) != 1 || status.Models[0] != "stealth/ox-alpha" {
		t.Fatalf("Models = %#v", status.Models)
	}

	status.Models[0] = "changed-by-caller"
	if got := s.Status().Models[0]; got != "stealth/ox-alpha" {
		t.Fatalf("Status exposed mutable model configuration: %q", got)
	}
}

func TestEmbeddedChatAPIStyleFailsClosed(t *testing.T) {
	for _, cfg := range []Config{
		{Addr: "127.0.0.1:0", APIStyle: "guessed", Dialer: configTestDialer{}},
		{Addr: "127.0.0.1:0", Models: []string{"bad\nmodel"}, Dialer: configTestDialer{}},
	} {
		if _, err := New(cfg); err == nil {
			t.Fatalf("New(%+v) accepted invalid embedded-chat configuration", cfg)
		}
	}
}

func TestEmbeddedChatDefaultsToAnthropic(t *testing.T) {
	s, err := New(Config{Addr: "127.0.0.1:0", Dialer: configTestDialer{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Status().APIStyle; got != APIStyleAnthropic {
		t.Fatalf("APIStyle = %q, want %q", got, APIStyleAnthropic)
	}
}
