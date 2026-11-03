// SPDX-License-Identifier: AGPL-3.0-only
// Sign-in (demo / demo), the session, two-factor and passkey settings,
// sessions list, and the capabilities the UI uses to show features.
import { http } from "msw";
import type { AuthState, Capabilities, Me } from "@/app/session";
import { api, body, fail, json, notInDemo } from "../core/http";
import { persisted } from "../core/store";
import { DAY, HOUR, MIN } from "../core/rand";
import { DEMO_USER, HOST } from "./host";

const session = persisted("session", () => ({ authenticated: false, since: 0 }));
const account = persisted("account", () => ({
  totp: true,
  recoveryLeft: 8,
  passkeys: [] as { id: string; name: string; createdAt: string; lastUsed?: string }[],
}));

export const isSignedIn = () => session.get().authenticated;

// Public endpoints (everything else needs a session, like the real server).
const PUBLIC = ["/api/auth/state", "/api/auth/login", "/api/auth/second-factor", "/api/auth/passkey/login/begin", "/api/auth/passkey/login/finish", "/api/auth/logout"];

/** Answers 401 for API calls without a session, then lets other handlers run. */
export const guard = http.all("/api/*", ({ request }) => {
  const path = new URL(request.url).pathname;
  if (PUBLIC.includes(path) || isSignedIn()) return undefined;
  return fail(401, "unauthorized", "Not signed in");
});

const recoveryCodes = () =>
  Array.from({ length: 10 }, () =>
    Array.from({ length: 2 }, () => Math.random().toString(36).slice(2, 7)).join("-"),
  );

export const authHandlers = [
  api.get("/api/auth/state", () =>
    json<AuthState & { container: boolean }>({
      authenticated: isSignedIn(),
      dev: false,
      pam: true,
      container: false,
      passkeys: account.get().passkeys.length > 0,
      secure: true,
    }),
  ),

  api.post("/api/auth/login", async ({ request }) => {
    const { username, password } = await body<{ username?: string; password?: string }>(request);
    if ((username ?? "").trim() !== DEMO_USER || password !== "demo") {
      return fail(401, "invalid_credentials", "Invalid username or password. In the demo, use demo / demo.");
    }
    session.set({ authenticated: true, since: Date.now() });
    return json({ next: "done" });
  }),

  api.post("/api/auth/second-factor", () => {
    session.set({ authenticated: true, since: Date.now() });
    return json({ next: "done" });
  }),
  api.post("/api/auth/passkey/login/begin", () => notInDemo("Passkey sign-in")),
  api.post("/api/auth/passkey/login/finish", () => notInDemo("Passkey sign-in")),

  api.post("/api/auth/logout", () => {
    session.set({ authenticated: false, since: 0 });
    return json({ ok: true });
  }),

  api.get("/api/auth/me", () => {
    const a = account.get();
    return json<Me>({
      user: DEMO_USER,
      adminGroup: "sudo",
      method: "password+totp",
      dev: false,
      csrf: "demo",
      totpEnabled: a.totp,
      totpSetupRequired: false,
      passkeys: a.passkeys.length,
      recoveryCodesLeft: a.recoveryLeft,
      expiresAt: new Date(Date.now() + 12 * HOUR).toISOString(),
    });
  }),

  api.post("/api/auth/totp/setup", () =>
    json({
      secret: "JBSWY3DPEHPK3PXP",
      uri: `otpauth://totp/Wargo:${DEMO_USER}@${HOST.hostname}?secret=JBSWY3DPEHPK3PXP&issuer=Wargo`,
    }),
  ),
  api.post("/api/auth/totp/enable", async ({ request }) => {
    const { code } = await body<{ code?: string }>(request);
    if (!/^\d{6}$/.test(code ?? "")) return fail(400, "invalid_code", "Enter the 6-digit code (any 6 digits work in the demo).");
    account.update((a) => {
      a.totp = true;
      a.recoveryLeft = 10;
    });
    return json({ recoveryCodes: recoveryCodes() });
  }),
  api.post("/api/auth/totp/disable", () => {
    account.update((a) => {
      a.totp = false;
    });
    return json({ ok: true });
  }),
  api.post("/api/auth/recovery-codes", () => {
    account.update((a) => {
      a.recoveryLeft = 10;
    });
    return json({ codes: recoveryCodes(), recoveryCodes: recoveryCodes() });
  }),

  api.get("/api/auth/passkeys", () => json(account.get().passkeys)),
  api.post("/api/auth/passkeys/register/begin", () => notInDemo("Adding a passkey")),
  api.post("/api/auth/passkeys/register/finish", () => notInDemo("Adding a passkey")),
  api.del("/api/auth/passkeys/:id", ({ params }) => {
    account.update((a) => {
      a.passkeys = a.passkeys.filter((p) => p.id !== params.id);
    });
    return json({ ok: true });
  }),

  api.get("/api/auth/sessions", () => {
    const ua = navigator.userAgent;
    const since = session.get().since || Date.now();
    return json([
      { id: "a1b2c3d4e5f6", createdAt: new Date(since).toISOString(), lastSeen: new Date().toISOString(), ip: "192.168.1.42", ua, method: "password+totp", current: true },
      {
        id: "9f8e7d6c5b4a",
        createdAt: new Date(Date.now() - 2 * DAY).toISOString(),
        lastSeen: new Date(Date.now() - 35 * MIN).toISOString(),
        ip: "100.101.42.7",
        ua: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1",
        method: "passkey",
        current: false,
      },
    ]);
  }),
  api.post("/api/auth/sessions/revoke-others", () => json({ revoked: 1 })),

  api.get("/api/capabilities", () =>
    json<Capabilities>({
      version: `${__WARGO_DEMO_REF__} (demo)`,
      elevation: { sudo: true, passkey: false },
      container: false,
      docker: { available: true, version: "27.3.1", apiVersion: "1.47", compose: true, updateCheckIntervalMs: 6 * HOUR, icons: true },
      prowlarr: { available: true, configured: true, hasTransmissionClient: true, version: "1.28.2.4885" },
      transmission: { available: true, configured: true, version: "4.0.6" },
      launcher: launcherCapability(),
      pkg: { backend: "apt", available: true, distro: "debian" },
      files: {
        roots: [
          { id: "0", kind: "place", name: "Home", path: "/home/demo" },
          { id: "1", kind: "place", name: "DATA", path: "/DATA" },
          { id: "m-root", kind: "drive", name: "System", path: "/" },
          { id: "m-data", kind: "drive", name: "DATA", path: "/DATA" },
          { id: "m-backup", kind: "drive", name: "Backup", path: "/mnt/backup" },
        ],
        trash: true,
        maxEditSize: 4 << 20,
        chunkSize: 8 << 20,
        showMounts: true,
        crossRoot: true,
        bookmarks: true,
      },
      terminal: { available: true, shell: "/bin/bash", tmux: false, sudo: true, docker: true, maxSessions: 8 },
    }),
  ),
];

/** The launcher feature fills this in (placement and on/off live there). */
export let launcherCapability: () => Capabilities["launcher"] = () => ({ enabled: true, placement: "overview" });
export function setLauncherCapability(fn: typeof launcherCapability) {
  launcherCapability = fn;
}
