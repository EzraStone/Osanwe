package main

import (
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunRefusesAMisconfiguredStorefront(t *testing.T) {
	key := func(value string) func(string) string {
		return func(name string) string {
			if name == "OSANWE_BTCPAY_CREATE_API_KEY" {
				return value
			}
			return ""
		}
	}
	valid := []string{"-btcpay", "https://pay.example", "-btcpay-store", "Store123", "-amount", "1.00", "-currency", "USD"}
	with := func(overrides ...string) []string {
		args := append([]string{}, valid...)
		for i := 0; i+1 < len(overrides); i += 2 {
			replaced := false
			for j := 0; j+1 < len(args); j += 2 {
				if args[j] == overrides[i] {
					args[j+1], replaced = overrides[i+1], true
				}
			}
			if !replaced {
				args = append(args, overrides[i], overrides[i+1])
			}
		}
		return args
	}
	tests := []struct {
		name   string
		args   []string
		getenv func(string) string
		want   string
	}{
		{"no API key", valid, key(""), "OSANWE_BTCPAY_CREATE_API_KEY is required"},
		{"API key that would inject a header", valid, key("abc\r\nX-Injected: 1"), "safe for an HTTP header"},
		{"plain HTTP to a remote BTCPay", with("-btcpay", "http://pay.example"), key("k"), "must use HTTPS"},
		{"credentials in the endpoint", with("-btcpay", "https://user:pass@pay.example"), key("k"), "must not contain user information"},
		{"no store", with("-btcpay-store", ""), key("k"), "store ID is required"},
		{"no amount", with("-amount", ""), key("k"), "amount must be a positive exact decimal"},
		{"negative amount", with("-amount", "-1.00"), key("k"), "amount must be a positive exact decimal"},
		{"malformed currency", with("-currency", "US$"), key("k"), "currency must contain"},
		{"no invoice ceiling", with("-max-invoices-per-minute", "0"), key("k"), "must be positive"},
		{"unknown flag", []string{"-free"}, key("k"), "flag provided but not defined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"-addr", "127.0.0.1:0"}, test.args...)
			done := make(chan error, 1)
			go func() { done <- run(args, test.getenv) }()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("run error = %v, want one containing %q", err, test.want)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("checkout started serving instead of refusing")
			}
		})
	}
}

func TestRunTreatsHelpAsSuccess(t *testing.T) {
	if err := run([]string{"-h"}, func(string) string { return "" }); err != nil {
		t.Fatalf("-h returned %v, want nil", err)
	}
}

// A storefront that cannot bind its port must not first announce that it is
// listening. The address was logged before ListenAndServe tried to bind, so an
// operator whose port was taken read "checkout listening" followed by an exit.
func TestRunDoesNotClaimToListenOnAPortItCannotBind(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = stderr })
	logged := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		logged <- string(out)
	}()

	done := make(chan error, 1)
	go func() {
		done <- run([]string{
			"-addr", taken.Addr().String(), "-btcpay", "https://pay.example", "-btcpay-store", "Store123",
			"-amount", "1.00", "-currency", "USD",
		}, func(name string) string {
			if name == "OSANWE_BTCPAY_CREATE_API_KEY" {
				return "k"
			}
			return ""
		})
	}()
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("checkout served on a port that was already taken")
	}
	w.Close()
	os.Stderr = stderr
	out := <-logged

	if strings.Contains(out, "checkout listening") {
		t.Fatalf("logged that it was listening, then failed to bind (%v):\n%s", err, out)
	}
	if err == nil || !strings.HasPrefix(err.Error(), "checkout: listen ") {
		t.Fatalf("run error = %v, want the bind failure", err)
	}
}
