// SPDX-License-Identifier: AGPL-3.0-only
// Setup text for connecting an agent to Wargo's MCP endpoint.

/** The MCP endpoint on origin. */
export function mcpURL(origin: string): string {
  return origin.replace(/\/+$/, "") + "/mcp";
}

/** Claude Code's command to add Wargo as an HTTP MCP server. */
export function claudeCode(url: string, token: string): string {
  return `claude mcp add --transport http wargo ${url} --header "Authorization: Bearer ${token}"`;
}

/** The common mcpServers JSON block, for clients configured with a file. */
export function genericJSON(url: string, token: string): string {
  return JSON.stringify({ mcpServers: { wargo: { type: "http", url, headers: { Authorization: `Bearer ${token}` } } } }, null, 2);
}

/** A line for the agent's own standing instructions, so it picks Wargo unprompted. */
export function standingInstruction(hostname: string): string {
  return `For anything about ${hostname}, use the Wargo tools first.`;
}
