// SPDX-License-Identifier: AGPL-3.0-only
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";

/** What an agent may do; each level includes the ones before it. */
export type Level = "read" | "operate" | "admin";

export const levels: { value: Level; label: string; hint: string }[] = [
  { value: "read", label: "Read only", hint: "Tools that look but change nothing." },
  { value: "operate", label: "Everyday actions", hint: "Also start or stop containers and add or remove torrents (including their downloaded files), without admin rights." },
  { value: "admin", label: "Admin requests", hint: "Also ask for admin actions, each of which waits for your approval." },
];

export const levelLabel = (l: Level) => levels.find((x) => x.value === l)?.label ?? l;

export interface MCPSettings {
  enabled: boolean;
  network: "local" | "anywhere";
  /** How long an admin request waits for approval, 1 to 60. */
  approvalMinutes: number;
}

export interface Agent {
  id: string;
  name: string;
  level: Level;
  createdAt: string;
  /** Go's zero time when never used. */
  lastUsedAt: string;
  lastIp: string;
}

export type ApprovalState = "pending" | "approved" | "denied" | "expired";

/** An agent's admin request waiting for the owner (internal/mcp Pending). */
export interface Approval {
  id: string;
  agent: { id: string; name: string; tool: string };
  agentId: string;
  tool: string;
  /** The tool's arguments, as the agent sent them. */
  args?: unknown;
  /** The exact internal request that runs on approval. */
  method: string;
  path: string;
  body?: unknown;
  /** Written by the tool; may include text the agent supplied. */
  summary: string;
  created: string;
  expires: string;
  state: ApprovalState;
  status?: number;
  result?: unknown;
  jobId?: string;
  /** Set while the approved request is being served (state is still pending). */
  running?: boolean;
}

const settingsKey = ["mcp", "settings"] as const;
const agentsKey = ["mcp", "agents"] as const;
const approvalsKey = ["mcp", "approvals"] as const;

export function useMCPSettings() {
  return useQuery({ queryKey: settingsKey, queryFn: () => api.get<MCPSettings>("/api/mcp/settings") });
}

export function useSaveMCPSettings() {
  const qc = useQueryClient();
  return useMutation({
    // Saves run one at a time, so a quick switch after a field change can't
    // land before it and be overwritten by the older whole-object PUT.
    scope: { id: "mcp-settings" },
    mutationFn: (s: MCPSettings) => api.put<MCPSettings>("/api/mcp/settings", s),
    onSuccess: (s) => {
      qc.setQueryData(settingsKey, s);
      qc.invalidateQueries({ queryKey: ["capabilities"] });
    },
  });
}

export function useAgents(enabled = true) {
  return useQuery({ queryKey: agentsKey, queryFn: () => api.get<Agent[]>("/api/mcp/agents"), enabled });
}

/**
 * Creates an agent. Called directly rather than through a mutation, so the
 * token lives only in the dialog's state and never in a cache.
 */
export function createAgent(name: string, level: Level) {
  return api.post<{ agent: Agent; token: string }>("/api/mcp/agents", { name, level });
}

export function useUpdateAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...patch }: { id: string; name?: string; level?: Level }) => api.patch(`/api/mcp/agents/${encodeURIComponent(id)}`, patch),
    onSettled: () => qc.invalidateQueries({ queryKey: agentsKey }),
  });
}

export function useRevokeAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.del(`/api/mcp/agents/${encodeURIComponent(id)}`),
    // Revoking denies the agent's waiting requests too.
    onSettled: () => Promise.all([qc.invalidateQueries({ queryKey: agentsKey }), qc.invalidateQueries({ queryKey: approvalsKey })]),
  });
}

export function invalidateAgents(qc: ReturnType<typeof useQueryClient>) {
  return qc.invalidateQueries({ queryKey: agentsKey });
}

export function useApprovals(enabled = true) {
  return useQuery({ queryKey: approvalsKey, queryFn: () => api.get<Approval[]>("/api/mcp/approvals"), refetchInterval: 10_000, enabled });
}

export function useApproval(id: string) {
  return useQuery({
    queryKey: [...approvalsKey, id],
    queryFn: () => api.get<Approval>(`/api/mcp/approvals/${encodeURIComponent(id)}`),
    retry: false,
    // Another tab or device may decide it; look again while it waits.
    refetchInterval: (q) => (q.state.data?.state === "pending" ? 5_000 : false),
  });
}

/** Approve or deny request id; the answer is the request as it now is. */
export function useDecide(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (decision: "approve" | "deny") => api.post<Approval>(`/api/mcp/approvals/${encodeURIComponent(id)}/${decision}`),
    onSuccess: (p) => qc.setQueryData([...approvalsKey, id], p),
    onSettled: () => qc.invalidateQueries({ queryKey: approvalsKey }),
  });
}
