// SPDX-License-Identifier: AGPL-3.0-only

package dashicons

// WebPorts are container-side ports that usually serve a web UI, in
// order of preference.
var WebPorts = []int{80, 443, 8080, 8443, 3000, 8000, 8096, 32400, 9091, 9696, 8989, 7878, 8686, 5055, 8123, 9000, 9443, 5000, 8081, 8888}

// KnownPorts are the default web UI ports of popular images (by icon
// slug); used for host networking and `network_mode: container:<id>`.
var KnownPorts = map[string]int{
	"transmission": 9091, "qbittorrent": 8080, "deluge": 8112, "prowlarr": 9696, "sonarr": 8989,
	"radarr": 7878, "lidarr": 8686, "readarr": 8787, "bazarr": 6767, "jackett": 9117, "sabnzbd": 8080,
	"nzbget": 6789, "flaresolverr": 8191, "plex": 32400, "jellyfin": 8096, "emby": 8096,
	"portainer": 9000, "home-assistant": 8123, "overseerr": 5055, "jellyseerr": 5055, "tautulli": 8181,
	"syncthing": 8384, "uptime-kuma": 3001, "pi-hole": 80, "adguard-home": 3000,
}
