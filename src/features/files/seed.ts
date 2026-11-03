// SPDX-License-Identifier: AGPL-3.0-only
// The starting filesystem: a lived-in homelab home folder, a full DATA
// drive, a backup drive and a standard Debian root.
import { DAY, GB, HOUR, MB, MIN, seeded } from "../../core/rand";
import { HOME, HOST } from "../host";
import type { ArchiveInfo, Node, Tree } from "./vfs";
import * as T from "./texts";

interface Opts {
  mode?: number;
  uid?: number;
  gid?: number;
  /** Modified this long ago (ms). */
  ago?: number;
  size?: number;
  text?: string;
  gen?: string;
  arc?: ArchiveInfo;
}

const enc = new TextEncoder();
const r = seeded(7);

const parentOf = (abs: string) => {
  const i = abs.lastIndexOf("/");
  return i <= 0 ? "/" : abs.slice(0, i);
};

/** Demo-owned areas; everything else belongs to root. */
function defaultOwner(abs: string): [number, number] {
  if (abs === HOME || abs.startsWith(HOME + "/")) return [1000, 1000];
  if (abs.startsWith("/DATA/") || abs.startsWith("/mnt/backup/restic")) return [1000, 1000];
  return [0, 0];
}

class Builder {
  nodes: Tree = {};
  private now = Date.now();

  private ensure(abs: string) {
    if (abs === "/" && !this.nodes["/"]) this.nodes["/"] = { t: "d", mode: 0o755, mtime: this.now - 300 * DAY, uid: 0, gid: 0 };
    if (this.nodes[abs]) return;
    this.ensure(parentOf(abs));
    const [uid, gid] = defaultOwner(abs);
    this.nodes[abs] = { t: "d", mode: 0o755, mtime: this.now - Math.floor(r() * 90 * DAY), uid, gid };
  }

  private base(abs: string, o: Opts, def: number): Omit<Node, "t"> {
    this.ensure(parentOf(abs));
    const [uid, gid] = defaultOwner(abs);
    return {
      mode: o.mode ?? def,
      uid: o.uid ?? uid,
      gid: o.gid ?? o.uid ?? gid,
      mtime: Math.round(this.now - (o.ago ?? Math.floor(r() * 120 * DAY + HOUR))),
    };
  }

  dir(abs: string, o: Opts = {}) {
    const n: Node = { t: "d", ...this.base(abs, o, 0o755) };
    this.nodes[abs] = { ...this.nodes[abs], ...n };
    return this;
  }

  file(abs: string, o: Opts = {}) {
    const size = o.text !== undefined ? enc.encode(o.text).length : (o.size ?? 0);
    const n: Node = { t: "f", ...this.base(abs, o, 0o644), size };
    if (o.text !== undefined) n.text = o.text;
    if (o.gen) n.gen = o.gen;
    if (o.arc) n.arc = o.arc;
    this.nodes[abs] = n;
    return this;
  }

  link(abs: string, target: string, o: Opts = {}) {
    this.nodes[abs] = { t: "l", ...this.base(abs, o, 0o777), target, size: target.length };
    return this;
  }
}

const jitter = (base: number, spread = 0.3) => Math.round(base * (1 - spread / 2 + r() * spread));
const date = (msAgo: number) => new Date(Date.now() - msAgo).toISOString().slice(0, 10);
let imgSeed = 1;
const img = (style: string, w = 1600, h = 1067) => `img:${imgSeed++}:${style}:${w}x${h}`;

// ---- home ---------------------------------------------------------------------

function home(b: Builder) {
  const H = HOME;
  b.dir("/home", { uid: 0, ago: 200 * DAY });
  b.dir(H, { mode: 0o700, ago: 2 * HOUR });
  b.file(`${H}/.bashrc`, { text: T.BASHRC, ago: 60 * DAY });
  b.file(`${H}/.profile`, { text: T.PROFILE, ago: 200 * DAY });
  b.file(`${H}/.bash_history`, { text: T.BASH_HISTORY, mode: 0o600, ago: 3 * HOUR });
  b.file(`${H}/.gitconfig`, { text: T.GITCONFIG, ago: 150 * DAY });
  b.dir(`${H}/.ssh`, { mode: 0o700, ago: 30 * DAY });
  b.file(`${H}/.ssh/authorized_keys`, { text: T.AUTHORIZED_KEYS, mode: 0o600, ago: 30 * DAY });
  b.file(`${H}/.ssh/config`, { text: T.SSH_CONFIG, mode: 0o600, ago: 90 * DAY });
  b.file(`${H}/.ssh/known_hosts`, { size: 2662, mode: 0o600, ago: 12 * DAY });
  b.file(`${H}/.ssh/id_ed25519`, { size: 411, mode: 0o600, ago: 190 * DAY });
  b.file(`${H}/.ssh/id_ed25519.pub`, { text: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHomelabDemoKeyNotRealAtAll0000000000000 demo@homelab\n", ago: 190 * DAY });
  b.file(`${H}/.config/htop/htoprc`, { text: T.HTOPRC, ago: 20 * DAY });
  b.dir(`${H}/.config/rclone`, { mode: 0o700 });
  b.file(`${H}/.config/rclone/rclone.conf`, { text: T.RCLONE_CONF, mode: 0o600, ago: 80 * DAY });
  b.dir(`${H}/.config/restic`, { mode: 0o700 });
  b.file(`${H}/.config/restic/password`, { size: 45, mode: 0o600, ago: 180 * DAY });
  b.file(`${H}/.docker/config.json`, { text: '{\n  "auths": {},\n  "currentContext": "default"\n}\n', mode: 0o600 });
  b.dir(`${H}/.cache/pip`);
  b.file(`${H}/.cache/motd.legal-displayed`, { size: 0, ago: 190 * DAY });
  b.dir(`${H}/.local/bin`);
  b.dir(`${H}/.local/share/wargo/trash`, { mode: 0o700, ago: 6 * DAY });
  const stamp = (ms: number) => {
    const d = new Date(Date.now() - ms);
    const p = (n: number) => String(n).padStart(2, "0");
    return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`;
  };
  b.file(`${H}/.local/share/wargo/trash/${stamp(6 * DAY)}-old-compose.yaml`, { text: "# replaced by stacks/media/compose.yaml\nservices:\n  plex:\n    image: plexinc/pms-docker:latest\n", ago: 40 * DAY });
  b.file(`${H}/.local/share/wargo/trash/${stamp(11 * DAY)}-Screenshot draft.png`, { gen: img("ui", 1440, 900), size: 412_331, ago: 15 * DAY });

  // Documents
  b.file(`${H}/Documents/README.md`, { text: T.README, ago: 21 * DAY });
  b.file(`${H}/Documents/notes.md`, { text: T.NOTES, ago: 5 * HOUR });
  b.file(`${H}/Documents/budget-2026.csv`, { text: T.budgetCsv(), ago: 3 * DAY });
  b.file(`${H}/Documents/wishlist.txt`, { text: T.SHOPPING, ago: 9 * DAY });
  b.file(`${H}/Documents/Manuals/router-setup.md`, { text: T.ROUTER_NOTES, ago: 220 * DAY });
  b.file(`${H}/Documents/Manuals/ups-cp1500.txt`, { text: T.UPS_NOTES, ago: 180 * DAY });

  // Downloads
  const dl = `${H}/Downloads`;
  b.file(`${dl}/debian-12.8.0-amd64-netinst.iso`, { size: 663_748_608, ago: 30 * DAY });
  b.file(`${dl}/ubuntu-24.04.1-live-server-amd64.iso`, { size: 2_773_874_688, ago: 64 * DAY });
  b.file(`${dl}/proxmox-ve_8.3-1.iso`, { size: 1_394_565_120, ago: 12 * DAY });
  b.file(`${dl}/tailscale_1.76.6_amd64.deb`, { size: 29_351_204, ago: 18 * DAY });
  b.file(`${dl}/jellyfin-media-player_1.11.1-1_amd64-bookworm.deb`, { size: 51_022_336, ago: 44 * DAY });
  b.file(`${dl}/node-v22.11.0-linux-x64.tar.xz`, { size: 29_142_548, ago: 70 * DAY, arc: { fmt: "tar.xz" } });
  b.file(`${dl}/rclone-v1.68.2-linux-amd64.zip`, {
    size: 23_480_371,
    ago: 33 * DAY,
    arc: {
      fmt: "zip",
      members: [
        ["rclone-v1.68.2-linux-amd64", { t: "d", mode: 0o755, mtime: Date.now() - 40 * DAY, uid: 1000, gid: 1000 }],
        ["rclone-v1.68.2-linux-amd64/rclone", { t: "f", size: 64_528_536, mode: 0o755, mtime: Date.now() - 40 * DAY, uid: 1000, gid: 1000 }],
        ["rclone-v1.68.2-linux-amd64/rclone.1", { t: "f", size: 1_819_342, mode: 0o644, mtime: Date.now() - 40 * DAY, uid: 1000, gid: 1000 }],
        ["rclone-v1.68.2-linux-amd64/README.txt", { t: "f", text: "Rclone\n======\n\nRclone syncs your files to cloud storage.\n", size: 41, mode: 0o644, mtime: Date.now() - 40 * DAY, uid: 1000, gid: 1000 }],
      ],
    },
  });
  b.file(`${dl}/homepage-config.zip`, {
    size: 3_412,
    ago: 8 * DAY,
    arc: {
      fmt: "zip",
      members: [
        ["homepage", { t: "d", mode: 0o755, mtime: Date.now() - 9 * DAY, uid: 1000, gid: 1000 }],
        ["homepage/services.yaml", { t: "f", text: T.HOMEPAGE_SERVICES, size: T.HOMEPAGE_SERVICES.length, mode: 0o644, mtime: Date.now() - 9 * DAY, uid: 1000, gid: 1000 }],
        ["homepage/settings.yaml", { t: "f", text: "title: Homelab\ntheme: dark\ncolor: slate\n", size: 38, mode: 0o644, mtime: Date.now() - 9 * DAY, uid: 1000, gid: 1000 }],
      ],
    },
  });
  b.file(`${dl}/wallpaper-pack.zip`, {
    size: 9_842_110,
    ago: 4 * DAY,
    arc: {
      fmt: "zip",
      members: (["dawn", "lake", "desert"] as const).map((s, i) => [
        `wallpaper-${i + 1}-${s}.jpg`,
        { t: "f", gen: img(s, 1920, 1080), size: jitter(3.1 * MB), mode: 0o644, mtime: Date.now() - 30 * DAY, uid: 1000, gid: 1000 },
      ]),
    },
  });

  // Pictures
  const pic = `${H}/Pictures`;
  const walls: [string, string][] = [["aurora", "aurora"], ["alpenglow", "sunset"], ["dunes", "desert"], ["fjord-night", "night"], ["misty-forest", "forest"], ["glacier-lake", "lake"]];
  walls.forEach(([n, s]) => b.file(`${pic}/Wallpapers/${n}.jpg`, { gen: img(s, 1920, 1080), size: jitter(2.4 * MB), ago: jitter(70 * DAY) }));
  b.file(`${pic}/Screenshots/Screenshot 2026-10-12 at 21.04.33.png`, { gen: img("ui", 1440, 900), size: 486_220, ago: 22 * DAY });
  b.file(`${pic}/Screenshots/Screenshot 2026-10-30 at 08.41.10.png`, { gen: img("ui", 1440, 900), size: 512_904, ago: 4 * DAY });
  b.file(`${pic}/IMG_20261019_174211.jpg`, { gen: img("autumn"), size: jitter(4.2 * MB), ago: 15 * DAY });
  b.file(`${pic}/IMG_20261026_071533.jpg`, { gen: img("alpine"), size: jitter(4.2 * MB), ago: 8 * DAY });
  b.file(`${pic}/IMG_20261101_163002.jpg`, { gen: img("ocean"), size: jitter(4.2 * MB), ago: 2 * DAY });
  b.file(`${pic}/homelab-logo.svg`, { text: LOGO_SVG, ago: 100 * DAY });

  // Compose projects (same layout as the Containers page): ~/stacks/<project>,
  // except pihole's, which is root-owned in /opt/stacks/network.
  const dk = `${H}/stacks`;
  b.link(`${H}/docker`, "stacks", { ago: 200 * DAY });
  b.file(`${dk}/media/compose.yaml`, { text: T.COMPOSE_MEDIA, ago: 6 * DAY });
  b.file(`${dk}/media/.env`, { text: "TZ=Europe/Berlin\nPUID=1000\nPGID=1000\n", ago: 90 * DAY });
  for (const s of ["jellyfin/config", "jellyfin/cache", "sonarr", "radarr", "prowlarr", "transmission", "qbittorrent"]) b.dir(`${dk}/media/${s}`);
  b.file(`${dk}/immich/compose.yaml`, { text: T.COMPOSE_IMMICH, ago: 12 * DAY });
  b.file(`${dk}/immich/.env`, { text: T.ENV_IMMICH, mode: 0o600, ago: 12 * DAY });
  b.dir(`${dk}/immich/postgres`, { mode: 0o700, uid: 999, gid: 0 });
  b.file(`${dk}/nextcloud/compose.yaml`, { text: T.COMPOSE_NEXTCLOUD, ago: 45 * DAY });
  b.file(`${dk}/nextcloud/.env`, { text: T.ENV_NEXTCLOUD, mode: 0o600, ago: 45 * DAY });
  b.dir(`${dk}/nextcloud/html`, { uid: 33, gid: 33 });
  b.file(`${dk}/home/compose.yaml`, { text: T.COMPOSE_HOME, ago: 25 * DAY });
  b.dir(`${dk}/home/config`, { uid: 0 });
  b.file("/opt/stacks/network/compose.yaml", { text: T.COMPOSE_NETWORK, uid: 0, ago: 75 * DAY });
  b.dir("/opt/stacks/network/etc-pihole", { uid: 999, gid: 999 });
  b.dir("/opt/stacks/network/etc-dnsmasq.d", { uid: 0 });
  b.file(`${dk}/tools/compose.yaml`, { text: T.COMPOSE_TOOLS, ago: 16 * DAY });
  b.dir(`${dk}/tools/vaultwarden`, { uid: 0 });
  b.dir(`${dk}/tools/uptime-kuma`, { uid: 0 });

  // scripts
  b.file(`${H}/scripts/backup.sh`, { text: T.BACKUP_SH, mode: 0o755, ago: 10 * DAY });
  b.file(`${H}/scripts/update-containers.sh`, { text: T.UPDATE_SH, mode: 0o755, ago: 52 * DAY });
  b.file(`${H}/scripts/check-disks.sh`, { text: T.DISKS_SH, mode: 0o755, ago: 95 * DAY });

  b.link(`${H}/media`, "/DATA/Media", { ago: 160 * DAY });
}

const LOGO_SVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256">
  <defs>
    <linearGradient id="g" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#7dd3fc"/>
      <stop offset="1" stop-color="#6366f1"/>
    </linearGradient>
  </defs>
  <rect width="256" height="256" rx="56" fill="#0f172a"/>
  <rect x="56" y="60" width="144" height="36" rx="8" fill="url(#g)"/>
  <rect x="56" y="110" width="144" height="36" rx="8" fill="url(#g)" opacity=".8"/>
  <rect x="56" y="160" width="144" height="36" rx="8" fill="url(#g)" opacity=".6"/>
  <circle cx="176" cy="78" r="6" fill="#22c55e"/>
  <circle cx="176" cy="128" r="6" fill="#22c55e"/>
  <circle cx="176" cy="178" r="6" fill="#f59e0b"/>
</svg>
`;

// ---- DATA ---------------------------------------------------------------------

const MOVIES: [string, number, string, number][] = [
  ["Dune Part Two", 2024, "2160p", 58.2], ["Oppenheimer", 2023, "1080p", 18.4], ["Blade Runner 2049", 2017, "2160p", 61.7],
  ["Arrival", 2016, "1080p", 12.1], ["Spirited Away", 2001, "1080p", 9.8], ["The Grand Budapest Hotel", 2014, "1080p", 8.7],
  ["Interstellar", 2014, "2160p", 64.3], ["Mad Max Fury Road", 2015, "2160p", 49.9], ["Everything Everywhere All at Once", 2022, "1080p", 14.2],
  ["Parasite", 2019, "1080p", 11.5], ["The Matrix", 1999, "2160p", 52.0], ["Princess Mononoke", 1997, "1080p", 10.4],
  ["Inception", 2010, "1080p", 16.9], ["Whiplash", 2014, "1080p", 7.9], ["Past Lives", 2023, "1080p", 6.8],
  ["Knives Out", 2019, "1080p", 10.2], ["Moonlight", 2016, "1080p", 7.1], ["The Martian", 2015, "2160p", 45.3],
  ["Her", 2013, "1080p", 8.3], ["Big Buck Bunny", 2008, "1080p", 0.88], ["Sintel", 2010, "1080p", 1.2],
  ["Ex Machina", 2014, "1080p", 9.1], ["Coco", 2017, "2160p", 38.6], ["Top Gun Maverick", 2022, "2160p", 55.4],
];

const SHOWS: [string, number, number[], number][] = [
  ["Severance", 2022, [9, 10], 3.1], ["The Expanse", 2015, [10, 13], 2.2], ["Shogun", 2024, [10], 4.2],
  ["Andor", 2022, [12], 3.4], ["The Bear", 2022, [8, 10], 1.4], ["Planet Earth III", 2023, [8], 4.8], ["Bluey", 2018, [26], 0.31],
];

const POSTER_STYLES = ["desert", "night", "sunset", "ocean", "forest", "alpine", "aurora", "lake", "snow", "autumn", "fjord", "dawn"];

function data(b: Builder) {
  b.dir("/DATA", { uid: 1000, ago: 1 * HOUR });
  b.dir("/DATA/lost+found", { mode: 0o700, uid: 0, ago: 400 * DAY });

  // Movies
  MOVIES.forEach(([title, year, q, gb], i) => {
    const folder = `/DATA/Media/Movies/${title} (${year})`;
    const ago = jitter((30 + i * 17) * DAY);
    b.file(`${folder}/${title} (${year}) - ${q}.mkv`, { size: Math.round(gb * GB), ago });
    b.file(`${folder}/poster.jpg`, { gen: img(POSTER_STYLES[i % POSTER_STYLES.length], 600, 900), size: jitter(380_000), ago });
    b.file(`${folder}/movie.nfo`, { text: T.nfo(title, String(year)), ago });
    if (i % 3 === 0) b.file(`${folder}/${title} (${year}).en.srt`, { text: T.subtitles(title), ago });
    b.dir(folder, { ago });
  });

  // TV
  SHOWS.forEach(([show, year, seasons, gb], si) => {
    const base = `/DATA/Media/TV/${show} (${year})`;
    seasons.forEach((eps, s) => {
      const season = `${base}/Season ${String(s + 1).padStart(2, "0")}`;
      const sAgo = jitter((200 - s * 120 - si * 9) * DAY + 10 * DAY);
      for (let e = 1; e <= eps; e++) {
        const code = `S${String(s + 1).padStart(2, "0")}E${String(e).padStart(2, "0")}`;
        b.file(`${season}/${show} - ${code} - ${q(gb)}.mkv`, { size: jitter(gb * GB, 0.25), ago: Math.max(HOUR, sAgo - e * 7 * DAY) });
      }
      b.dir(season, { ago: Math.max(HOUR, sAgo - eps * 7 * DAY) });
    });
    b.file(`${base}/poster.jpg`, { gen: img(POSTER_STYLES[(si + 5) % POSTER_STYLES.length], 600, 900), size: jitter(420_000) });
    b.file(`${base}/tvshow.nfo`, { text: `<?xml version="1.0" encoding="UTF-8"?>\n<tvshow>\n  <title>${show}</title>\n  <year>${year}</year>\n</tvshow>\n` });
  });
  b.dir("/DATA/Media/Music");
  b.dir("/DATA/Media/Audiobooks");

  // Photos
  const trips: [string, string, string[]][] = [
    ["2025", "2025-03 Iceland", ["snow", "aurora", "fjord", "snow", "aurora", "lake"]],
    ["2025", "2025-07 Dolomites", ["alpine", "alpine", "sunset", "lake", "dawn", "alpine", "forest"]],
    ["2025", "2025-10 Autumn walks", ["autumn", "forest", "autumn", "dawn", "autumn"]],
    ["2026", "2026-02 Snow day", ["snow", "snow", "dawn", "night"]],
    ["2026", "2026-05 Lake Garda", ["lake", "sunset", "lake", "ocean", "dawn", "lake"]],
    ["2026", "2026-08 Norway", ["fjord", "fjord", "alpine", "night", "ocean", "fjord", "lake", "sunset"]],
  ];
  trips.forEach(([year, trip, styles], ti) => {
    const [, mm] = trip.split(" ")[0].split("-");
    const tAgo = (year === "2025" ? 300 : 120) * DAY - ti * 20 * DAY;
    styles.forEach((s, k) => {
      const dd = String(3 + k * 2).padStart(2, "0");
      const hh = String(8 + ((k * 3) % 11)).padStart(2, "0");
      const name = `IMG_${year}${mm}${dd}_${hh}${String(10 + k * 7).padStart(2, "0")}${String(11 + k * 5).padStart(2, "0")}.jpg`;
      b.file(`/DATA/Photos/${year}/${trip}/${name}`, { gen: img(s), size: jitter(4.6 * MB), ago: Math.max(DAY, tAgo - k * DAY) });
    });
  });

  // Backups
  for (let d = 0; d < 6; d++) {
    const when = d * DAY + 8 * HOUR + 30 * MIN;
    b.file(`/DATA/Backups/immich-db-${date(when)}.sql.zst`, { size: jitter(182 * MB, 0.05), ago: when, arc: { fmt: "tar.zst" } });
    b.file(`/DATA/Backups/vaultwarden-${date(when)}.tar.zst`, { size: jitter(12.4 * MB, 0.05), ago: when, arc: { fmt: "tar.zst" } });
  }
  for (let w = 0; w < 4; w++) {
    const when = w * 7 * DAY + 2 * DAY + 9 * HOUR;
    b.file(`/DATA/Backups/nextcloud-${date(when)}.tar.zst`, { size: jitter(2.1 * GB, 0.08), ago: when, arc: { fmt: "tar.zst" } });
    b.file(`/DATA/Backups/home-assistant-${date(when)}.tar`, {
      size: jitter(86 * MB, 0.1),
      ago: when,
      arc: {
        fmt: "tar",
        members: [
          ["backup.json", { t: "f", text: `{"slug":"ha${w}","name":"Automatic backup ${date(when)}","type":"full","homeassistant":{"version":"2024.11.3"}}\n`, size: 96, mode: 0o644, mtime: Date.now() - when, uid: 1000, gid: 1000 }],
          ["homeassistant.tar.gz", { t: "f", size: jitter(85 * MB, 0.1), mode: 0o644, mtime: Date.now() - when, uid: 1000, gid: 1000 }],
        ],
      },
    });
  }

  // Downloads (torrent clients)
  b.file("/DATA/Downloads/complete/ubuntu-24.04.1-desktop-amd64.iso", { size: 6_114_656_256, ago: 20 * DAY });
  b.file("/DATA/Downloads/complete/debian-12.8.0-amd64-DVD-1.iso", { size: 3_994_091_520, ago: 26 * DAY });
  b.file("/DATA/Downloads/complete/archlinux-2026.11.01-x86_64.iso", { size: 1_221_197_824, ago: 2 * DAY });
  b.file("/DATA/Downloads/complete/Big Buck Bunny (2008) [1080p]/Big.Buck.Bunny.2008.1080p.BluRay.x264.mkv", { size: 928_670_754, ago: 60 * DAY });
  b.file("/DATA/Downloads/complete/Big Buck Bunny (2008) [1080p]/README.txt", { text: "Big Buck Bunny (c) copyright 2008, Blender Foundation\nwww.bigbuckbunny.org\nLicensed under CC BY 3.0\n", ago: 60 * DAY });
  b.file("/DATA/Downloads/incomplete/Sintel.2010.2160p.mkv.part", { size: 2_211_840_000, ago: 4 * MIN });

  // Paperless media
  b.dir("/DATA/Documents/paperless/originals");
  b.dir("/DATA/Documents/paperless/consume");

  // App data owned by containers
  b.dir("/DATA/immich", { uid: 0, ago: 3 * HOUR });
  for (const d of ["upload", "thumbs", "encoded-video", "profile", "backups"]) b.dir(`/DATA/immich/${d}`, { uid: 0 });
  for (let k = 0; k < 4; k++) {
    b.file(`/DATA/immich/library/admin/2026/2026-10-${String(12 + k * 4)}/IMG_202610${12 + k * 4}_0912${10 + k}.jpg`, { gen: img(["lake", "autumn", "dawn", "forest"][k]), size: jitter(4.4 * MB), uid: 0, ago: (20 - k * 4) * DAY });
  }
  for (const p of ["/DATA/immich/library", "/DATA/immich/library/admin", "/DATA/immich/library/admin/2026"]) b.dir(p, { uid: 0 });
  for (let k = 0; k < 4; k++) b.dir(`/DATA/immich/library/admin/2026/2026-10-${String(12 + k * 4)}`, { uid: 0, ago: (20 - k * 4) * DAY });
  b.dir("/DATA/nextcloud", { mode: 0o770, uid: 33, gid: 33, ago: 20 * MIN });
  b.file("/DATA/nextcloud/nextcloud.log", { size: 18 * MB, mode: 0o640, uid: 33, gid: 33 });
}

const q = (gb: number) => (gb > 3 ? "2160p" : gb > 0.5 ? "1080p" : "720p");

// ---- backup drive ---------------------------------------------------------------

function backup(b: Builder) {
  b.dir("/mnt", { uid: 0, ago: 300 * DAY });
  b.dir("/mnt/backup", { uid: 0, ago: 8 * HOUR });
  const R = "/mnt/backup/restic";
  b.dir(R, { mode: 0o700, ago: 8 * HOUR });
  b.file(`${R}/config`, { size: 155, mode: 0o400, ago: 380 * DAY });
  const hex = (n: number) => Array.from({ length: n }, () => Math.floor(r() * 16).toString(16)).join("");
  for (const sub of ["0a", "1f", "3c", "5e", "7b", "9d", "b2", "e4"]) {
    for (let k = 0; k < 4; k++) b.file(`${R}/data/${sub}/${sub}${hex(62)}`, { size: jitter(17 * MB, 0.2), mode: 0o400, ago: jitter(90 * DAY) });
  }
  for (let k = 0; k < 6; k++) b.file(`${R}/index/${hex(64)}`, { size: jitter(380_000), mode: 0o400 });
  b.file(`${R}/keys/${hex(64)}`, { size: 451, mode: 0o400, ago: 380 * DAY });
  b.dir(`${R}/locks`, { mode: 0o700 });
  for (let d = 0; d < 7; d++) b.file(`${R}/snapshots/${hex(64)}`, { size: 412, mode: 0o400, ago: d * DAY + 8 * HOUR });
  for (let d = 0; d < 5; d++) b.dir(`/mnt/backup/snapshots/restic-${date(d * DAY + 8 * HOUR)}`, { uid: 0, ago: d * DAY + 8 * HOUR });
  for (let w = 1; w <= 3; w++) b.dir(`/mnt/backup/snapshots/DATA-weekly-${date(w * 7 * DAY)}`, { uid: 0, ago: w * 7 * DAY });
  b.dir("/mnt/backup/photos/2025", { uid: 1000 });
  b.dir("/mnt/backup/photos/2026", { uid: 1000 });
  b.dir("/mnt/backup/photos", { uid: 1000, ago: 3.5 * HOUR });
}

// ---- system root -------------------------------------------------------------------

function system(b: Builder) {
  const old = 300 * DAY;
  for (const [l, t] of [["/bin", "usr/bin"], ["/sbin", "usr/sbin"], ["/lib", "usr/lib"], ["/lib64", "usr/lib64"]]) b.link(l, t, { uid: 0, ago: 380 * DAY });
  b.link("/vmlinuz", `boot/vmlinuz-${HOST.kernel}`, { uid: 0, ago: 24 * DAY });
  b.link("/initrd.img", `boot/initrd.img-${HOST.kernel}`, { uid: 0, ago: 24 * DAY });
  for (const d of ["/dev", "/run", "/media", "/srv"]) b.dir(d, { ago: d === "/run" || d === "/dev" ? 23 * DAY : old });
  b.dir("/proc", { mode: 0o555, ago: 23 * DAY });
  b.dir("/sys", { mode: 0o555, ago: 23 * DAY });
  b.dir("/root", { mode: 0o700, ago: 40 * DAY });
  b.dir("/tmp", { mode: 0o1777, ago: 10 * MIN });
  b.dir("/tmp/systemd-private-3f9a1c-systemd-timesyncd.service-Xk2p", { mode: 0o700 });
  b.dir("/tmp/tmux-1000", { mode: 0o700, uid: 1000, gid: 1000, ago: 3 * HOUR });
  b.file("/tmp/wargo-demo.txt", { text: "Scratch file. /tmp is cleared on reboot.\n", uid: 1000, gid: 1000, ago: 50 * MIN });
  b.dir("/var/tmp", { mode: 0o1777 });

  // boot
  const k = HOST.kernel;
  b.file(`/boot/vmlinuz-${k}`, { size: 8_154_624, ago: 24 * DAY });
  b.file(`/boot/initrd.img-${k}`, { size: 68_331_520, ago: 24 * DAY });
  b.file(`/boot/config-${k}`, { size: 273_415, ago: 24 * DAY });
  b.file(`/boot/System.map-${k}`, { size: 83, ago: 24 * DAY });
  b.file("/boot/grub/grub.cfg", { text: T.GRUB_CFG, mode: 0o444, ago: 24 * DAY });
  b.dir("/boot/efi", { mode: 0o700, ago: old });
  b.dir("/boot/efi/EFI/debian", { mode: 0o700 });

  // etc
  b.link("/etc/os-release", "../usr/lib/os-release", { uid: 0, ago: 60 * DAY });
  b.file("/usr/lib/os-release", { text: T.OS_RELEASE, ago: 60 * DAY });
  b.file("/etc/hostname", { text: `${HOST.hostname}\n`, ago: old });
  b.file("/etc/hosts", { text: T.HOSTS, ago: 3 * HOUR });
  b.file("/etc/fstab", { text: T.FSTAB, ago: 210 * DAY });
  b.file("/etc/resolv.conf", { text: T.RESOLV, ago: 23 * DAY });
  b.file("/etc/passwd", { text: T.PASSWD, ago: 190 * DAY });
  b.file("/etc/group", { text: T.GROUP, ago: 190 * DAY });
  b.file("/etc/shadow", { text: T.SHADOW, mode: 0o640, gid: 42, ago: 190 * DAY });
  b.file("/etc/sudoers", { text: T.SUDOERS, mode: 0o440, ago: 300 * DAY });
  b.dir("/etc/sudoers.d", { mode: 0o750 });
  b.file("/etc/timezone", { text: "Europe/Berlin\n", ago: old });
  b.link("/etc/localtime", "/usr/share/zoneinfo/Europe/Berlin", { uid: 0, ago: old });
  b.file("/usr/share/zoneinfo/Europe/Berlin", { size: 2298, ago: 90 * DAY });
  b.file("/etc/debian_version", { text: "12.8\n", ago: 60 * DAY });
  b.file("/etc/issue", { text: "Debian GNU/Linux 12 \\n \\l\n\n", ago: 60 * DAY });
  b.file("/etc/motd", { text: T.MOTD, ago: old });
  b.file("/etc/crontab", { text: T.CRONTAB, ago: 70 * DAY });
  b.file("/etc/ssh/sshd_config", { text: T.SSHD_CONFIG, ago: 180 * DAY });
  b.file("/etc/ssh/ssh_host_ed25519_key", { size: 411, mode: 0o600, ago: old });
  b.file("/etc/ssh/ssh_host_ed25519_key.pub", { text: `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHostKeyDemoNotReal000000000000000000000 root@${HOST.hostname}\n`, ago: old });
  b.file("/etc/apt/sources.list.d/debian.sources", { text: T.DEBIAN_SOURCES, ago: 200 * DAY });
  b.file("/etc/apt/sources.list.d/docker.sources", { text: T.DOCKER_SOURCES, ago: 200 * DAY });
  b.file("/etc/apt/keyrings/docker.asc", { size: 3817, ago: 200 * DAY });
  b.file("/etc/docker/daemon.json", { text: T.DAEMON_JSON, ago: 150 * DAY });
  b.file("/etc/systemd/system/backup-photos.service", { text: T.BACKUP_SERVICE, ago: 35 * DAY });
  b.file("/etc/systemd/system/backup-photos.timer", { text: T.BACKUP_TIMER, ago: 35 * DAY });
  b.file("/etc/network/interfaces", { text: T.INTERFACES, ago: 300 * DAY });
  b.dir("/etc/wargo", { mode: 0o750 });
  b.file("/etc/wargo/wargo.toml", { text: T.WARGO_TOML, mode: 0o640, ago: 14 * DAY });
  b.dir("/etc/ssl/private", { mode: 0o710, gid: 103 });
  b.dir("/etc/ssl/certs");
  b.dir("/etc/cron.daily");
  b.dir("/etc/default");
  b.dir("/etc/logrotate.d");

  // usr
  const bins: [string, number, number?][] = [
    ["bash", 1_265_648], ["cat", 44_016], ["cp", 151_152], ["curl", 280_296], ["df", 101_904], ["du", 109_616], ["find", 224_896],
    ["git", 3_986_520], ["grep", 186_904], ["htop", 335_024], ["less", 192_352], ["ls", 151_344], ["mv", 147_096], ["nano", 283_800],
    ["python3.11", 6_831_736], ["rsync", 523_248], ["sed", 113_640], ["ssh", 1_046_048], ["scp", 133_120], ["tar", 531_464],
    ["tmux", 1_042_896], ["vim.basic", 3_830_648], ["wget", 535_584], ["zstd", 1_309_808], ["journalctl", 84_112], ["systemctl", 1_391_032],
    ["apt", 18_816], ["apt-get", 51_976], ["dpkg", 318_432], ["docker", 36_401_312], ["dockerd", 101_253_376], ["containerd", 39_055_640],
    ["restic", 27_156_632], ["tailscale", 28_901_120], ["btrfs", 1_198_400],
    ["sudo", 281_624, 0o4755], ["passwd", 68_248, 0o4755], ["su", 72_000, 0o4755], ["mount", 59_704, 0o4755], ["umount", 35_128, 0o4755],
  ];
  bins.forEach(([n, s, m]) => b.file(`/usr/bin/${n}`, { size: s, mode: m ?? 0o755, ago: jitter(120 * DAY) }));
  b.link("/usr/bin/python3", "python3.11", { uid: 0 });
  b.link("/usr/bin/vi", "vim.basic", { uid: 0 });
  b.link("/usr/bin/vim", "vim.basic", { uid: 0 });
  for (const [n, s] of [["sshd", 1_247_656], ["smartctl", 870_120], ["fdisk", 158_216], ["ip", 784_456], ["cron", 68_360], ["useradd", 147_160], ["visudo", 248_976], ["tailscaled", 54_120_448]] as const) {
    b.file(`/usr/sbin/${n}`, { size: s, mode: 0o755, ago: jitter(120 * DAY) });
  }
  b.file("/usr/local/bin/wargo", { size: 18_734_080, mode: 0o755, ago: 14 * DAY });
  b.file("/usr/lib/x86_64-linux-gnu/libc.so.6", { size: 1_922_136 });
  b.file("/usr/lib/x86_64-linux-gnu/libssl.so.3", { size: 694_472 });
  b.file("/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2", { size: 210_968 });
  b.dir(`/usr/lib/modules/${k}`, { ago: 24 * DAY });
  b.dir("/usr/lib/systemd/system");
  b.link("/usr/lib64/ld-linux-x86-64.so.2", "/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2", { uid: 0 });
  b.file("/usr/share/bash-completion/bash_completion", { size: 103_219 });
  for (const d of ["doc", "man", "fonts", "keyrings", "locale"]) b.dir(`/usr/share/${d}`);
  b.dir("/usr/include");
  b.dir("/usr/src");

  // opt
  b.file("/opt/containerd/bin/containerd-shim-runc-v2", { size: 9_043_968, mode: 0o755 });
  b.dir("/opt/containerd/lib");

  // var
  b.file("/var/log/syslog", { text: T.syslog(), mode: 0o640, gid: 4, ago: 2 * MIN });
  b.file("/var/log/syslog.1", { size: 1_884_205, mode: 0o640, gid: 4, ago: DAY });
  b.file("/var/log/syslog.2.gz", { size: 214_872, mode: 0o640, gid: 4, ago: 2 * DAY });
  b.file("/var/log/auth.log", { text: T.authLog(), mode: 0o640, gid: 4, ago: 40 * MIN });
  b.file("/var/log/kern.log", { size: 412_088, mode: 0o640, gid: 4, ago: 2 * HOUR });
  b.file("/var/log/dpkg.log", { text: T.dpkgLog(), ago: 3 * DAY });
  b.file("/var/log/backup.log", { text: T.backupLog(), uid: 1000, gid: 1000, ago: 8 * HOUR });
  b.file("/var/log/apt/history.log", { size: 48_211, ago: 3 * DAY });
  b.dir("/var/log/journal", { mode: 0o2755, gid: 999, ago: 23 * DAY });
  b.dir("/var/log/journal/0d3c9b5e8f7a4c21b6e94d1a7f203e58", { mode: 0o2755, gid: 999, ago: 2 * MIN });
  b.dir("/var/lib/docker", { mode: 0o710, ago: 10 * MIN });
  b.dir("/var/lib/wargo", { mode: 0o700, ago: 5 * MIN });
  b.dir("/var/lib/apt/lists");
  b.file("/var/cache/apt/archives/docker-ce_5%3a27.3.1-1~debian.12~bookworm_amd64.deb", { size: 26_102_212, ago: 12 * DAY });
  b.file("/var/cache/apt/archives/tailscale_1.76.6_amd64.deb", { size: 29_351_204, ago: 18 * DAY });
  b.dir("/var/backups");
  b.dir("/var/www", { ago: old });
  b.dir("/var/spool/cron/crontabs", { mode: 0o1730, gid: 0 });
}

export function seedTree(): Tree {
  const b = new Builder();
  system(b);
  home(b);
  data(b);
  backup(b);
  // Mount points keep their own owners.
  b.nodes["/DATA"].uid = 1000;
  b.nodes["/DATA"].gid = 1000;
  return b.nodes;
}
