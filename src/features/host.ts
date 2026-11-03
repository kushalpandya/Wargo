// SPDX-License-Identifier: AGPL-3.0-only
// The made-up server every demo page describes. Keep the facts here so
// pages agree with each other (hostname, CPU, drives, addresses, user).

export const DEMO_USER = "demo";
export const HOME = "/home/demo";

export const HOST = {
  hostname: "homelab",
  os: "Debian GNU/Linux 12 (bookworm)",
  osId: "debian",
  kernel: "6.1.0-28-amd64",
  arch: "amd64",
  cpuModel: "AMD Ryzen 7 5700G with Radeon Graphics",
  cores: 8,
  threads: 16,
  memTotal: 32 * 1024 ** 3,
  swapTotal: 4 * 1024 ** 3,
  /** Booted 23 days, 4 hours ago. */
  bootTime: Math.floor(Date.now() / 1000) - (23 * 86400 + 4 * 3600 + 17 * 60),
  lanIp: "192.168.1.20",
  lanCidr: "192.168.1.20/24",
  tailscaleIp: "100.84.12.31",
  publicIp: "203.0.113.42",
  gateway: "192.168.1.1",
} as const;

/**
 * The containers running on the demo server. Containers, the launcher's
 * "Add from containers", Events and Torrents all use this list.
 */
export interface DemoContainer {
  name: string;
  image: string;
  /** dashboard-icons slug (src/icons/<slug>.svg). */
  icon?: string;
  /** Published web UI port on the host. */
  port?: number;
  /** Launcher group / compose project. */
  group: string;
  state: "running" | "exited" | "paused";
  /** Docker HEALTHCHECK result, when the image has one. */
  health?: "healthy" | "unhealthy" | "starting";
  /** A newer image is available. */
  update?: string;
  exitCode?: number;
}

export const CONTAINERS: DemoContainer[] = [
  { name: "jellyfin", image: "jellyfin/jellyfin:10.10.3", icon: "jellyfin", port: 8096, group: "media", state: "running", health: "unhealthy", update: "10.10.5" },
  { name: "sonarr", image: "lscr.io/linuxserver/sonarr:4.0.11", icon: "sonarr", port: 8989, group: "media", state: "running" },
  { name: "radarr", image: "lscr.io/linuxserver/radarr:5.15.1", icon: "radarr", port: 7878, group: "media", state: "running", update: "5.16.3" },
  { name: "prowlarr", image: "lscr.io/linuxserver/prowlarr:1.28.2", icon: "prowlarr", port: 9696, group: "media", state: "running" },
  { name: "transmission", image: "lscr.io/linuxserver/transmission:4.0.6", icon: "transmission", port: 9091, group: "media", state: "running" },
  { name: "qbittorrent", image: "lscr.io/linuxserver/qbittorrent:5.0.2", icon: "qbittorrent", port: 8085, group: "media", state: "exited", exitCode: 143 },
  { name: "immich-server", image: "ghcr.io/immich-app/immich-server:v1.122.0", icon: "immich", port: 2283, group: "immich", state: "running", health: "healthy", update: "v1.123.0" },
  { name: "immich-postgres", image: "tensorchord/pgvecto-rs:pg14-v0.2.0", icon: "postgres", group: "immich", state: "running", health: "healthy" },
  { name: "immich-redis", image: "redis:7.4-alpine", icon: "redis", group: "immich", state: "running", health: "healthy" },
  { name: "nextcloud", image: "nextcloud:29-apache", icon: "nextcloud", port: 8080, group: "nextcloud", state: "running", update: "29.0.10-apache" },
  { name: "nextcloud-cron", image: "nextcloud:29-apache", icon: "nextcloud", group: "nextcloud", state: "exited", exitCode: 1 },
  { name: "home-assistant", image: "ghcr.io/home-assistant/home-assistant:2024.11.3", icon: "home-assistant", port: 8123, group: "home", state: "running" },
  { name: "pihole", image: "pihole/pihole:2024.07.0", icon: "pi-hole", port: 8053, group: "network", state: "running", health: "healthy" },
  { name: "vaultwarden", image: "vaultwarden/server:1.32.5", icon: "vaultwarden", port: 8222, group: "tools", state: "running", health: "healthy" },
  { name: "uptime-kuma", image: "louislam/uptime-kuma:1.23.15", icon: "uptime-kuma", port: 3001, group: "tools", state: "running", health: "healthy" },
  { name: "paperless", image: "ghcr.io/paperless-ngx/paperless-ngx:2.13.5", icon: "paperless-ngx", port: 8010, group: "tools", state: "paused" },
];
