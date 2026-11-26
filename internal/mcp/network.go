// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"net"
	"net/http"

	"github.com/kushalpandya/wargo/internal/httpx"
)

// Local says whether ip is on the private network: loopback, the RFC 1918
// ranges, IPv6 unique local addresses (fc00::/7), link-local, or one of the
// extra networks from mcp.allowed_networks.
func Local(ip net.IP, extra []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	for _, n := range extra {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// admits says whether the network setting lets r's client in.
//
// Under "local", a request carrying any forwarding header (httpx.Forwarded)
// whose peer is not a trusted proxy is refused: that is what a tunnel
// (cloudflared connects from 127.0.0.1) or a proxy the owner has not listed
// looks like, and its peer address says nothing about where the client
// really is. A proxy on this host that adds no forwarding header at all
// cannot be told apart from a local client, and is admitted.
func (s *Service) admits(r *http.Request) bool {
	if s.Settings().Network == NetworkAnywhere {
		return true
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	client := s.proxy.ClientIP(r)
	// ClientIP only looks past the peer when the peer is trusted. When it
	// still answers with the peer, the forwarding headers were not believed
	// (or named nothing usable), so the real client is unknown.
	if httpx.Forwarded(r) && client == peer {
		return false
	}
	return Local(net.ParseIP(client), s.extraNets)
}
