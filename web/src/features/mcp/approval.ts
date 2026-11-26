// SPDX-License-Identifier: AGPL-3.0-only
// Pure helpers for the agents settings and the approval page.

/** A time the server sends for "never" (Go's zero time), or no usable time. */
export function isNever(t: string | undefined | null): boolean {
  if (!t) return true;
  const ms = Date.parse(t);
  return !Number.isFinite(ms) || ms <= 0;
}

/** The time left until expires as "m:ss" (rounded up), or null once it has passed. */
export function timeLeft(expires: string, now: number): string | null {
  const s = Math.ceil((Date.parse(expires) - now) / 1000);
  if (!(s > 0)) return null;
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

const messages: Record<string, string> = {
  approval_in_progress: "This request is already being approved. Wait a moment and reload.",
  already_approved: "This request was already approved.",
  already_denied: "This request was already denied.",
  expired: "This request has expired. The agent can ask again.",
  not_found: "No such request. It may have expired, or Wargo restarted since it was made.",
  mcp_disabled: "Agents are turned off. Turn on Allow agents (MCP) in Settings › Agents, then approve again.",
  agent_revoked: "The agent that asked for this has been revoked, so nothing will run.",
};

/**
 * What to tell the owner when approving or denying failed, or null when
 * there is nothing to say (they canceled the passkey or password prompt).
 */
export function approvalErrorMessage(err: { code: string; status: number; message: string }): string | null {
  if (err.code === "elevation_canceled") return null;
  if (err.code === "forbidden_level") return `${err.message} Raise its access in Settings › Agents, then approve again.`;
  return messages[err.code] ?? err.message;
}

/** v as indented JSON ("" for nothing). */
export function prettyJSON(v: unknown): string {
  if (v === undefined || v === null) return "";
  return JSON.stringify(v, null, 2);
}
