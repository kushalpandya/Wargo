# Publishing Wargo

Wargo listens on `127.0.0.1:2207`. Put a TLS-terminating proxy in front of it
and tell Wargo which public origins to expect:

```toml
# /etc/wargo/config.toml
[auth]
origins = ["https://wargo.example.com"]
trusted_proxies = ["127.0.0.1/32", "::1/128"]
```

Wargo needs **WebSockets** (live metrics, terminal, container logs) and
unbuffered streaming for downloads. Uploads are sent in 8 MiB chunks, so body
limits only need to exceed one chunk.

## cloudflared (Cloudflare Tunnel)

```yaml
# ~/.cloudflared/config.yml
tunnel: <tunnel-id>
credentials-file: /home/<you>/.cloudflared/<tunnel-id>.json
ingress:
  - hostname: wargo.example.com
    service: http://127.0.0.1:2207
  - service: http_status:404
```

WebSockets work out of the box. Strongly consider protecting the hostname with
**Cloudflare Access** (Zero Trust → Access → Applications → Self-hosted) so
only your identity reaches Wargo's login page at all.

## nginx

See [`deploy/nginx/wargo.conf`](../deploy/nginx/wargo.conf). The important
parts are the `Upgrade`/`Connection` headers, `proxy_read_timeout` for long
lived terminals, `proxy_buffering off`, and `X-Forwarded-Proto` so Wargo marks
cookies `Secure` and enables HSTS.

## Caddy

```caddyfile
wargo.example.com {
	reverse_proxy 127.0.0.1:2207
}
```

## Serving Wargo as the homepage on port 80

The recommended setup keeps a proxy on 80/443 and routes `/` to Wargo while
CasaOS (or anything else) moves to another port or subdomain. If you really
want Wargo itself on port 80, set `listen = ":80"` and uncomment
`AmbientCapabilities=CAP_NET_BIND_SERVICE` in the systemd unit — it still runs
as your user, not root. Plain HTTP on a LAN works, but passkeys and the
clipboard API need HTTPS (or `localhost`).
