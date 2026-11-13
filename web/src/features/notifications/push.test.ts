// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/features/notifications/push.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { isIOS, notificationOpen, pushIsOn, pushRefusal, pushSupport, sessionEndsNote } from "./push.ts";

const good = { secure: true, sw: true, push: true, standalone: false, ios: false, permission: "default" };

test("pushSupport is ok when everything is in place", () => {
  assert.equal(pushSupport(good), "ok");
  assert.equal(pushSupport({ ...good, permission: "granted" }), "ok");
  assert.equal(pushSupport({ ...good, ios: true, standalone: true }), "ok");
});

test("pushSupport needs HTTPS first", () => {
  assert.equal(pushSupport({ ...good, secure: false }), "not-https");
  assert.equal(pushSupport({ ...good, secure: false, sw: false, push: false, ios: true }), "not-https");
});

test("pushSupport asks iPhone users to install from the Home Screen first", () => {
  // Safari outside the installed app has no PushManager at all.
  assert.equal(pushSupport({ ...good, ios: true, push: false }), "install-first");
  assert.equal(pushSupport({ ...good, ios: true }), "install-first");
});

test("pushSupport reports a browser without Web Push as unsupported", () => {
  assert.equal(pushSupport({ ...good, push: false }), "unsupported");
  assert.equal(pushSupport({ ...good, sw: false }), "unsupported");
});

test("pushSupport reports a denied permission", () => {
  assert.equal(pushSupport({ ...good, permission: "denied" }), "denied");
  assert.equal(pushSupport({ ...good, ios: true, standalone: true, permission: "denied" }), "denied");
});

test("isIOS spots iPhone, iPad and iPadOS posing as a Mac", () => {
  assert.equal(isIOS("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)", 5), true);
  assert.equal(isIOS("Mozilla/5.0 (iPad; CPU OS 16_0 like Mac OS X)", 5), true);
  assert.equal(isIOS("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Safari/605.1.15", 5), true);
  assert.equal(isIOS("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Safari/605.1.15", 0), false);
  assert.equal(isIOS("Mozilla/5.0 (X11; Linux x86_64) Chrome/130.0", 0), false);
  assert.equal(isIOS("Mozilla/5.0 (Linux; Android 15) Chrome/130.0 Mobile", 5), false);
});

const key = "BNuktCnpOwjEpu7JPywwHsrRu8f5E8WsrBC2Hx16wtMjz9tF3sgbfzzCb_Wcd7twfuE8HSFPEG8ut0HPoUS1yEk";
const raw = (b64url: string) => Uint8Array.from(atob(b64url.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0)).buffer;

test("pushIsOn needs the server registration and a browser subscription for the same key", () => {
  assert.equal(pushIsOn(undefined, null), false);
  assert.equal(pushIsOn({ publicKey: key, subscribed: false }, raw(key)), false);
  // The browser dropped its subscription (site data cleared, expired): off, so it can be enabled again.
  assert.equal(pushIsOn({ publicKey: key, subscribed: true }, null), false);
  assert.equal(pushIsOn({ publicKey: key, subscribed: true }, raw(key)), true);
  // Made for another server key: nothing sent with the current key would arrive.
  assert.equal(pushIsOn({ publicKey: key, subscribed: true }, new Uint8Array(65).buffer), false);
});

test("notificationOpen accepts only the worker's message with a same-origin path or none", () => {
  assert.deepEqual(notificationOpen({ type: "notification-open", link: null }), { link: null });
  assert.deepEqual(notificationOpen({ type: "notification-open", link: "/containers/plex" }), { link: "/containers/plex" });
  assert.deepEqual(notificationOpen({ type: "notification-open", link: "//evil.example/x" }), { link: null });
  assert.deepEqual(notificationOpen({ type: "notification-open", link: "https://evil.example/" }), { link: null });
  assert.deepEqual(notificationOpen({ type: "notification-open", link: "/\\evil.example" }), { link: null });
  assert.equal(notificationOpen({ type: "SKIP_WAITING" }), null);
  assert.equal(notificationOpen(null), null);
  assert.equal(notificationOpen("notification-open"), null);
});

test("sessionEndsNote warns that push stops with a sign-in that is not remembered", () => {
  const tail = ", and push stops with it. Sign in with Keep me signed in to get push while you are away.";
  assert.equal(sessionEndsNote(30), "This sign-in ends 30 minutes after you last use Wargo here" + tail);
  assert.equal(sessionEndsNote(1), "This sign-in ends 1 minute after you last use Wargo here" + tail);
  assert.equal(sessionEndsNote(60), "This sign-in ends 1 hour after you last use Wargo here" + tail);
  assert.equal(sessionEndsNote(120), "This sign-in ends 2 hours after you last use Wargo here" + tail);
  assert.equal(sessionEndsNote(90), "This sign-in ends 90 minutes after you last use Wargo here" + tail);
  // An older server without idleMinutes: the default.
  assert.equal(sessionEndsNote(undefined), "This sign-in ends 30 minutes after you last use Wargo here" + tail);
});

test("pushRefusal explains a dismissed or denied permission prompt", () => {
  assert.deepEqual(pushRefusal("default"), { title: "Notifications were not allowed" });
  assert.deepEqual(pushRefusal("denied"), {
    title: "Notifications were not allowed",
    body: "Notifications for Wargo are blocked on this device. Allow them in your system settings, then come back here.",
  });
});
