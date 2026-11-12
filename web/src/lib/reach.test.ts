// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/lib/reach.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { classifyResponse, fetchFailure, shouldRetry } from "./reach.ts";

const api = (status: number, contentType: string) => classifyResponse({ path: "/api/auth/state", status, contentType });

test("Wargo's own JSON is never unreachable", () => {
  assert.equal(api(200, "application/json"), "ok");
  assert.equal(api(401, "application/json; charset=utf-8"), "error"); // still signs out
  assert.equal(api(503, "application/json"), "error");
});

test("proxy and tunnel answers count as unreachable", () => {
  assert.equal(api(200, "text/html; charset=UTF-8"), "unreachable"); // a login or error page
  assert.equal(api(502, "text/html"), "unreachable");
  assert.equal(api(530, ""), "unreachable"); // Cloudflare tunnel down
});

test("non-JSON 4xx is an ordinary error", () => {
  assert.equal(api(404, "text/plain"), "error");
});

test("shouldRetry retries network failures but not Wargo's own 4xx", () => {
  assert.equal(shouldRetry(0, { status: 0, code: "unreachable" }), true);
  assert.equal(shouldRetry(1, { status: 0, code: "unreachable" }), true);
  assert.equal(shouldRetry(2, { status: 0, code: "unreachable" }), false);
  assert.equal(shouldRetry(0, { status: 404, code: "not_found" }), false);
  assert.equal(shouldRetry(0, { status: 502, code: "error" }), true);
  assert.equal(shouldRetry(0, null), true); // not an API error
});

test("fetchFailure keeps cancellations, reports timeouts and network errors as unreachable", () => {
  const abort = new DOMException("aborted", "AbortError");
  const timeout = new DOMException("timed out", "TimeoutError");
  assert.equal(fetchFailure(abort, undefined), "abort");
  assert.equal(fetchFailure(abort, abort), "abort");
  assert.equal(fetchFailure(abort, timeout), "unreachable"); // a browser that rejects a timeout as AbortError
  assert.equal(fetchFailure(timeout, timeout), "unreachable");
  assert.equal(fetchFailure(new TypeError("Load failed"), undefined), "unreachable");
});
