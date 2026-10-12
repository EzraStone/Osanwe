package main

import (
	"reflect"
	"testing"
)

func TestIdentityHostsAddsAConcreteBindHostOnce(t *testing.T) {
	loopback := []string{"localhost", "127.0.0.1", "::1"}
	tests := []struct {
		addr string
		want []string
	}{
		{"0.0.0.0:8443", loopback},
		{"[::]:8443", loopback},
		{":8443", loopback},
		{"not an address", loopback},
		{"127.0.0.1:8443", loopback},
		{"[::1]:8443", loopback},
		{"localhost:8443", loopback},
		{"relay.example:8443", append(append([]string{}, loopback...), "relay.example")},
		{"203.0.113.7:8443", append(append([]string{}, loopback...), "203.0.113.7")},
	}
	for _, test := range tests {
		if got := identityHosts(test.addr); !reflect.DeepEqual(got, test.want) {
			t.Errorf("identityHosts(%q) = %q, want %q", test.addr, got, test.want)
		}
	}
}

func TestDisplayAddrNamesWildcardBinds(t *testing.T) {
	tests := map[string]string{
		"0.0.0.0:8443":       "<this-host>:8443",
		"[::]:8443":          "<this-host>:8443",
		":8443":              "<this-host>:8443",
		"203.0.113.7:8443":   "203.0.113.7:8443",
		"relay.example:8443": "relay.example:8443",
		"not an address":     "not an address",
	}
	for addr, want := range tests {
		if got := displayAddr(addr); got != want {
			t.Errorf("displayAddr(%q) = %q, want %q", addr, got, want)
		}
	}
}
