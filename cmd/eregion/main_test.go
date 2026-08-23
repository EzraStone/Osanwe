package main

import "testing"

func TestSelectAuthorizationMode(t *testing.T) {
	tests := []struct {
		name                string
		open                bool
		btcpay, invite      string
		want                authorizationMode
		wantMutualExclusion bool
	}{
		{name: "none", want: authorizationModeNone},
		{name: "open", open: true, want: authorizationModeOpen},
		{name: "BTCPay", btcpay: "https://pay.example", want: authorizationModeBTCPay},
		{name: "invite", invite: "invite-manifest.json", want: authorizationModeInvite},
		{name: "open and BTCPay", open: true, btcpay: "https://pay.example", wantMutualExclusion: true},
		{name: "open and invite", open: true, invite: "invite-manifest.json", wantMutualExclusion: true},
		{name: "BTCPay and invite", btcpay: "https://pay.example", invite: "invite-manifest.json", wantMutualExclusion: true},
		{name: "all", open: true, btcpay: "https://pay.example", invite: "invite-manifest.json", wantMutualExclusion: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectAuthorizationMode(test.open, test.btcpay, test.invite)
			if test.wantMutualExclusion {
				if err == nil {
					t.Fatalf("mode = %v, want mutual-exclusion error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectAuthorizationMode: %v", err)
			}
			if got != test.want {
				t.Fatalf("mode = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidateInviteCapacityFlag(t *testing.T) {
	for _, test := range []struct {
		name     string
		mode     authorizationMode
		capacity int
		wantErr  bool
	}{
		{name: "invite pinned", mode: authorizationModeInvite, capacity: 100},
		{name: "invite missing", mode: authorizationModeInvite, wantErr: true},
		{name: "invite negative", mode: authorizationModeInvite, capacity: -1, wantErr: true},
		{name: "BTCPay unchanged", mode: authorizationModeBTCPay},
		{name: "open unchanged", mode: authorizationModeOpen},
		{name: "capacity without manifest", mode: authorizationModeBTCPay, capacity: 100, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateInviteCapacityFlag(test.mode, test.capacity)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateLoadedInviteCapacity(t *testing.T) {
	if err := validateLoadedInviteCapacity(100, 100); err != nil {
		t.Fatalf("matching capacity: %v", err)
	}
	for _, actual := range []int{99, 101, 100_000} {
		if err := validateLoadedInviteCapacity(100, actual); err == nil {
			t.Fatalf("actual capacity %d was accepted against pin 100", actual)
		}
	}
}
