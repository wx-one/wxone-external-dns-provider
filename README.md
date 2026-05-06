# WX ONE ExternalDNS Webhook

Webhook provider for `external-dns` backed by the WX ONE API.

## Auth

Use a WX ONE API key id and secret. Do not use a normal account login.

## Environment

- `WX1_HOST` or `--host`
- `WX1_API_KEY_ID` or `--username`
- `WX1_API_KEY_SECRET` or `--password`
- `WX1_TENANT` or `--tenant` (defaults to `wizardtales.com`)
- `WX1_PROJECT_ID` or `--project-id` (optional)
- `WX1_ZONE_ID` or `--zone-id` (optional)
- `WX1_DOMAIN_FILTERS` or `--filters` (optional, comma-separated)
- `WX1_AUTH_CACHE_TTL` or `--auth-cache-ttl` (default `4h`)

## Ports

- Provider: `127.0.0.1:8888`
- Health: `0.0.0.0:8080`

## ExternalDNS flags

Run ExternalDNS with `--provider=webhook` and `--webhook-provider-url=http://127.0.0.1:8888`.
The default record type set works for A, AAAA, CNAME, NS, SRV, and TXT.

If you only want `default`, add `--namespace=default` and bind RBAC just for that namespace.
The `wx1-dns01-auth` secret must also live in `default` for the sidecar example.

## Build

```bash
go build ./...
docker build -t wxone/external-dns-webhook-wx1:dev .
```

## ExternalDNS

Run `external-dns` with `--provider=webhook` and `--webhook-provider-url=http://127.0.0.1:8888`.

See `deploy/sidecar-example.yaml` for a minimal pod template.

Apply `deploy/rbac.yaml` first.
