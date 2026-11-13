// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Push on this device: whether it can receive push, and turning it on or off.
 * The service worker (web/sw/sw.js) shows what arrives.
 */
import type { api } from "@/lib/api";

/** What the server says about this session's push registration. */
export interface PushState {
  /** The VAPID public key, base64url. */
  publicKey: string;
  subscribed: boolean;
}

export type PushSupport = "ok" | "not-https" | "unsupported" | "install-first" | "denied";

/** The API calls push needs (the app passes `api`; kept as a parameter so this file runs under node --test). */
export type PushClient = Pick<typeof api, "post" | "del" | "get">;

export const pushPath = "/api/notifications/push";

/**
 * Whether this device can receive push, and if not, the first thing in the
 * way. iOS only offers Web Push in the app opened from the Home Screen, so
 * Safari there has no PushManager until it is installed.
 */
export function pushSupport(env: {
  secure: boolean;
  sw: boolean;
  push: boolean;
  standalone: boolean;
  ios: boolean;
  permission: string;
}): PushSupport {
  if (!env.secure) return "not-https";
  if (env.ios && !env.standalone) return "install-first";
  if (!env.sw || !env.push) return "unsupported";
  if (env.permission === "denied") return "denied";
  return "ok";
}

/** What to do about notifications blocked on this device. */
export const deniedHint = "Notifications for Wargo are blocked on this device. Allow them in your system settings, then come back here.";

/** The toast for a permission prompt that did not end in "granted": dismissed ("default") or refused ("denied"). */
export function pushRefusal(permission: string): { title: string; body?: string } {
  const title = "Notifications were not allowed";
  return permission === "denied" ? { title, body: deniedHint } : { title };
}

/** iPhone or iPad, including iPadOS, which reports itself as a Mac but has a touch screen. */
export function isIOS(userAgent: string, maxTouchPoints: number): boolean {
  return /iPhone|iPad|iPod/.test(userAgent) || (/Macintosh/.test(userAgent) && maxTouchPoints > 1);
}

function keyBytes(base64url: string): Uint8Array<ArrayBuffer> {
  const b64 = base64url.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (base64url.length % 4)) % 4);
  const raw = atob(b64);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

function sameKey(a: ArrayBuffer | null, b: Uint8Array): boolean {
  if (!a || a.byteLength !== b.length) return false;
  const x = new Uint8Array(a);
  return x.every((v, i) => v === b[i]);
}

/**
 * Whether push really reaches this device: the server has this session
 * registered and the browser still holds a subscription for the server's
 * key (`localKey` is its applicationServerKey, null without one). A browser
 * can drop its subscription on its own, and then nothing would arrive.
 */
export function pushIsOn(server: PushState | undefined, localKey: ArrayBuffer | null): boolean {
  return !!server?.subscribed && sameKey(localKey, keyBytes(server.publicKey));
}

/** The key of this browser's push subscription, or null when it has none. */
export async function localPushKey(): Promise<ArrayBuffer | null> {
  if (!("serviceWorker" in navigator)) return null;
  const reg = await navigator.serviceWorker.getRegistration();
  const sub = await reg?.pushManager?.getSubscription();
  return sub?.options.applicationServerKey ?? null;
}

/**
 * What the push row says on a sign-in without "Keep me signed in": the
 * session ends idleMinutes after its last use (30 by default), and push to
 * this device stops with it.
 */
export function sessionEndsNote(idleMinutes: number | undefined): string {
  const m = idleMinutes && idleMinutes > 0 ? idleMinutes : 30;
  const span = m % 60 === 0 ? `${m / 60} hour${m === 60 ? "" : "s"}` : `${m} minute${m === 1 ? "" : "s"}`;
  return `This sign-in ends ${span} after you last use Wargo here, and push stops with it. Sign in with Keep me signed in to get push while you are away.`;
}

/** Clears the Home Screen icon's badge where the browser supports it (on sign-out, nothing should stay counted). */
export function clearAppBadge() {
  if (typeof navigator === "undefined" || !("clearAppBadge" in navigator)) return;
  try {
    navigator.clearAppBadge().catch(() => {});
  } catch {
    /* not allowed here (no installed app, no permission) */
  }
}

/**
 * Reads the worker's message after a notification tap on an open window:
 * where to go (a same-origin path), or null link for the panel. Anything
 * else is not that message.
 */
export function notificationOpen(data: unknown): { link: string | null } | null {
  if (typeof data !== "object" || data === null || (data as { type?: unknown }).type !== "notification-open") return null;
  const link = (data as { link?: unknown }).link;
  const path = typeof link === "string" && link.startsWith("/") && !link.startsWith("//") && !link.includes("\\");
  return { link: path ? link : null };
}

/** The service worker's registration, or an error if it never starts (it only runs in the built app). */
function registration(): Promise<ServiceWorkerRegistration> {
  return Promise.race([
    navigator.serviceWorker.ready,
    new Promise<never>((_, reject) =>
      setTimeout(() => reject(new Error("Wargo's service worker isn't running. Reload the page and try again.")), 10_000),
    ),
  ]);
}

/**
 * Asks for permission (call it straight from a click), then registers this
 * device for push. Returns null when permission wasn't granted; check
 * Notification.permission to tell a refusal from a dismissed prompt.
 */
export async function enablePush(client: PushClient): Promise<PushState | null> {
  if ((await Notification.requestPermission()) !== "granted") return null;
  const reg = await registration();
  const { publicKey } = await client.get<PushState>(pushPath);
  const key = keyBytes(publicKey);
  let sub = await reg.pushManager.getSubscription();
  // A subscription made for an older server key would be refused: replace it.
  if (sub && !sameKey(sub.options.applicationServerKey, key)) {
    await sub.unsubscribe();
    sub = null;
  }
  sub ??= await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
  return client.post<PushState>(pushPath, sub.toJSON());
}

/** Stops push to this device: the server forgets it, then the browser drops its subscription. */
export async function disablePush(client: PushClient): Promise<PushState> {
  const state = await client.del<PushState>(pushPath);
  const reg = "serviceWorker" in navigator ? await navigator.serviceWorker.getRegistration() : undefined;
  const sub = await reg?.pushManager.getSubscription();
  await sub?.unsubscribe().catch(() => {});
  return state;
}
