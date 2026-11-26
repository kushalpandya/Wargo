// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/features/mcp/approval.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { approvalErrorMessage, isNever, timeLeft } from "./approval.ts";

test("isNever: the zero time, empty and invalid times were never used", () => {
  assert.equal(isNever("0001-01-01T00:00:00Z"), true);
  assert.equal(isNever(""), true);
  assert.equal(isNever(undefined), true);
  assert.equal(isNever("not a time"), true);
  assert.equal(isNever("2026-11-25T10:00:00Z"), false);
});

test("timeLeft counts down in minutes and seconds", () => {
  const now = Date.parse("2026-11-25T10:00:00Z");
  assert.equal(timeLeft("2026-11-25T10:15:00Z", now), "15:00");
  assert.equal(timeLeft("2026-11-25T10:00:09.400Z", now), "0:10");
  assert.equal(timeLeft("2026-11-25T10:01:05Z", now), "1:05");
  assert.equal(timeLeft("2026-11-25T10:00:00Z", now), null);
  assert.equal(timeLeft("2026-11-25T09:59:00Z", now), null);
});

test("approvalErrorMessage explains each refusal", () => {
  const cases: [string, number, string][] = [
    ["approval_in_progress", 409, "This request is already being approved. Wait a moment and reload."],
    ["already_approved", 409, "This request was already approved."],
    ["already_denied", 409, "This request was already denied."],
    ["expired", 410, "This request has expired. The agent can ask again."],
    ["not_found", 404, "No such request. It may have expired, or Wargo restarted since it was made."],
    ["mcp_disabled", 409, "Agents are turned off. Turn on Allow agents (MCP) in Settings › Agents, then approve again."],
    ["agent_revoked", 409, "The agent that asked for this has been revoked, so nothing will run."],
  ];
  for (const [code, status, want] of cases) assert.equal(approvalErrorMessage({ code, status, message: "server text" }), want, code);
});

test("approvalErrorMessage keeps the server's words where they say more", () => {
  // forbidden_level names the tool.
  assert.equal(
    approvalErrorMessage({ code: "forbidden_level", status: 409, message: "The agent's access level no longer allows upgrade_homebrew." }),
    "The agent's access level no longer allows upgrade_homebrew. Raise its access in Settings › Agents, then approve again.",
  );
  assert.equal(approvalErrorMessage({ code: "boom", status: 500, message: "Something broke" }), "Something broke");
});

test("approvalErrorMessage is null when the owner canceled the credential prompt", () => {
  assert.equal(approvalErrorMessage({ code: "elevation_canceled", status: 403, message: "Canceled" }), null);
});
