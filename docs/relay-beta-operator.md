# Independent relay operator packet

This packet is for one person who is not the gateway/mint operator. Independence is a human and
organizational fact, not something the client can prove. Do not describe the beta as operator-split
until this relay has carried a successful synthetic request and the operator has confirmed their
role in writing.

## What the relay can and cannot see

The relay sees client source addresses, connection timing, volume, and the single allowed gateway
destination. TLS passes through it, so it cannot read prompts, answers, model names, tokens, or the
Groq credential. The gateway sees plaintext prompts and answers but, when the relay is genuinely
independent and non-colluding, does not see the client's source address.

This is experimental traffic using synthetic or deliberately non-sensitive prompts. The relay is
not an open proxy: its destination allowlist must contain only the beta gateway.

## Minimal host

- A small Linux VM with a stable public address, current security updates, and roughly 256 MB free
  memory is enough for ten text-only testers.
- TCP 8443 is open to invited clients. Outbound TCP 8444 is allowed only to the gateway.
- The operator controls the VM account, billing, logs, backups, and shutdown—not the gateway owner.
- Do not enable packet/content logging beyond ordinary aggregate host metrics. State exactly what
  the hosting provider records independently.

## Build and create the relay identity

```sh
git clone https://github.com/EzraStone/Osanwe.git
cd Osanwe
go test ./cmd/ranger ./internal/ranger ./internal/tunnel
go build -trimpath -o ranger ./cmd/ranger
sudo install -m 0755 ranger /usr/local/bin/ranger
sudo useradd --system --home /var/lib/osanwe-relay --create-home osanwe-relay
sudo install -d -o osanwe-relay -g osanwe-relay -m 0700 /var/lib/osanwe-relay/state
sudo -u osanwe-relay /usr/local/bin/ranger -dir /var/lib/osanwe-relay/state -pin
sudo -u osanwe-relay /usr/local/bin/ranger -dir /var/lib/osanwe-relay/state -identity
```

Generate the shared relay secret directly on the relay host and deliver it out of band. Never put
it in an issue, chat screenshot, shell history, repository, or service command line.

```sh
sudo -u osanwe-relay /usr/local/bin/ranger -gen-secret
```

## Service

Put the secret alone in `/etc/osanwe-relay.env`, owner-readable only:

```sh
OSANWE_RANGER_SECRET=replace-with-the-generated-secret
```

```ini
[Unit]
Description=Osanwe independent beta relay
After=network-online.target
Wants=network-online.target

[Service]
User=osanwe-relay
WorkingDirectory=/var/lib/osanwe-relay
EnvironmentFile=/etc/osanwe-relay.env
ExecStart=/usr/local/bin/ranger -dir /var/lib/osanwe-relay/state -addr 0.0.0.0:8443 -allow GATEWAY_HOST:8444
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/osanwe-relay/state

[Install]
WantedBy=multi-user.target
```

After `systemctl enable --now osanwe-relay`, send the beta operator only:

- public `host:8443` address;
- printed TLS pin;
- printed relay identity;
- shared secret through a separate private channel;
- hosting provider, jurisdiction, retention/logging statement, and a shutdown contact.

## Acceptance test

1. Gateway firewall allows only this relay's public `/32` address on TCP 8444.
2. A clean client pins the returned relay pin and gateway certificate.
3. A synthetic streaming request succeeds through the relay.
4. A request to any destination other than the exact gateway is refused.
5. Relay application logs contain no prompt, answer, token, provider key, or request body.
6. Stopping the relay makes the client fail closed; it does not connect directly to the gateway.

The gateway owner records these results and the operator's independence statement. Either party can
end the experiment immediately. Removing the relay requires revoking its firewall access and
issuing updated enrollment material.
