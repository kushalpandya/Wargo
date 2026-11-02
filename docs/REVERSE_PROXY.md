# Publishing Wargo

To use Wargo from outside your home network, put a proxy in front of it that
handles HTTPS, and tell Wargo which public addresses to expect:

```toml
# /etc/wargo/config.toml
[auth]
origins = ["https://wargo.example.com"]
trusted_proxies = ["127.0.0.1/32", "::1/128"]
```

For Docker, set the same values with `WARGO_ORIGINS=https://wargo.example.com`
and `WARGO_TRUSTED_PROXIES=127.0.0.1/32,::1/128`.

**`origins` is required behind a proxy.** Wargo refuses requests for
hostnames it doesn't know (HTTP 421 "Misdirected Request"), so a malicious
website can't reach it through DNS rebinding. IP addresses, `localhost`, the
server's own hostname and private names (`.local`, `.lan`, `.home.arpa`,
`.internal`) always work. Anything else must be listed in `origins`.

The proxy must also:

- pass on the original `Host` header,
- set `X-Forwarded-For`, because Wargo slows down sign-in attempts per
  visitor address,
- set `X-Forwarded-Proto: https`, so Wargo marks its cookies as HTTPS-only.

Cloudflare Tunnel, Caddy and the nginx example below do all of this.

Wargo also needs **WebSockets** (live metrics, the terminal, container logs)
and unbuffered streaming for downloads. Uploads are sent in 8 MiB pieces, so
any upload size limit only needs to be larger than that.

## Cloudflare Tunnel (cloudflared)

```yaml
# ~/.cloudflared/config.yml
tunnel: <tunnel-id>
credentials-file: /home/<you>/.cloudflared/<tunnel-id>.json
ingress:
  - hostname: wargo.example.com
    service: http://127.0.0.1:2207
  - service: http_status:404
```

WebSockets work without extra setup. We strongly recommend protecting the
hostname with **Cloudflare Access** (Zero Trust → Access → Applications →
Self-hosted), so only you can even reach Wargo's sign-in page.

## nginx

See [`deploy/nginx/wargo.conf`](../deploy/nginx/wargo.conf). The important
parts are the `Upgrade` and `Connection` headers (WebSockets), a long
`proxy_read_timeout` for terminals that stay open, `proxy_buffering off`, and
`X-Forwarded-Proto` so Wargo marks cookies as HTTPS-only and turns on HSTS.

## Caddy

```caddyfile
wargo.example.com {
	reverse_proxy 127.0.0.1:2207
}
```

## Serving Wargo on port 80

The recommended setup keeps a proxy on ports 80 and 443 that sends `/` to
Wargo, and moves CasaOS (or anything else) to another port or subdomain. If
you really want Wargo itself on port 80 (native install), set
`listen = ":80"` and uncomment `AmbientCapabilities=CAP_NET_BIND_SERVICE` in
the systemd unit. It still runs as your user, not root. Plain HTTP on your
local network works, but passkeys and copy/paste need HTTPS (or `localhost`).
