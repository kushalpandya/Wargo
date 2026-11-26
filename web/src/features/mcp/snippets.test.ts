// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/features/mcp/snippets.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { claudeCode, genericJSON, mcpURL, standingInstruction } from "./snippets.ts";

const url = "https://wargo.example/mcp";
const token = "wargo_abc123";

test("mcpURL is the origin's /mcp", () => {
  assert.equal(mcpURL("https://wargo.example"), "https://wargo.example/mcp");
  assert.equal(mcpURL("http://192.168.1.5:2207/"), "http://192.168.1.5:2207/mcp");
});

test("claudeCode is Claude Code's add command with the bearer header", () => {
  assert.equal(
    claudeCode(url, token),
    'claude mcp add --transport http wargo https://wargo.example/mcp --header "Authorization: Bearer wargo_abc123"',
  );
});

test("genericJSON is an mcpServers block for HTTP clients", () => {
  assert.equal(
    genericJSON(url, token),
    `{
  "mcpServers": {
    "wargo": {
      "type": "http",
      "url": "https://wargo.example/mcp",
      "headers": {
        "Authorization": "Bearer wargo_abc123"
      }
    }
  }
}`,
  );
});

test("standingInstruction names the host", () => {
  assert.equal(standingInstruction("deimos"), "For anything about deimos, use the Wargo tools first.");
});
