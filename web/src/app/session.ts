// SPDX-License-Identifier: AGPL-3.0-only
import { useQuery } from "@tanstack/react-query";
import { api, isUnreachable } from "@/lib/api";

export interface AuthState {
  authenticated: boolean;
  dev: boolean;
  pam: boolean;
  pamError?: string;
  passkeys: boolean;
  secure: boolean;
}

export interface Me {
  user: string;
  adminGroup: string;
  method: string;
  dev: boolean;
  csrf: string;
  totpEnabled: boolean;
  totpSetupRequired: boolean;
  passkeys: number;
  recoveryCodesLeft: number;
  expiresAt: string;
}

export function useAuthState() {
  return useQuery({
    queryKey: ["auth", "state"],
    // A server that doesn't answer within 10 s counts as unreachable (see app/Unreachable.tsx).
    queryFn: ({ signal }) =>
      api.get<AuthState>(
        "/api/auth/state",
        // AbortSignal.any needs iOS 17.4+; older browsers just wait for the network.
        typeof AbortSignal.any === "function" ? AbortSignal.any([signal, AbortSignal.timeout(10_000)]) : signal,
      ),
    staleTime: 30_000,
    // When Wargo can't be reached, app/Unreachable.tsx shows at once and retries on its own.
    retry: (n, err) => !isUnreachable(err) && n < 2,
  });
}

export function useMe(enabled = true) {
  return useQuery({ queryKey: ["auth", "me"], queryFn: () => api.get<Me>("/api/auth/me"), enabled, staleTime: 60_000 });
}

export interface Capabilities {
  version: string;
  elevation: { sudo: boolean; passkey: boolean; password?: boolean; mode?: "container"; noNewPrivsHint?: boolean; /** false where wargo-privd doesn't exist (macOS). */ privd?: boolean };
  /** Wargo runs in Docker (container mode). */
  container?: boolean;
  docker?: {
    available: boolean;
    error?: string;
    casaos?: boolean;
    version?: string;
    apiVersion?: string;
    compose?: boolean;
    /** Background image update-check interval (0 = disabled). */
    updateCheckIntervalMs?: number;
    /** Container logos are served at Container.icon. */
    icons?: boolean;
    /** Wargo can update its own container (container mode, socket mounted). */
    selfUpdate?: boolean;
  };
  prowlarr?: { available: boolean; configured: boolean; hasTransmissionClient: boolean; version?: string; error?: string };
  transmission?: { available: boolean; configured: boolean; version?: string; error?: string };
  plex?: { available: boolean; configured: boolean; version?: string; error?: string };
  launcher?: { enabled: boolean; placement: "overview" | "page" };
  /** supported: false where OS updates don't exist (macOS). */
  pkg?: { backend?: string; available: boolean; distro?: string; supported?: boolean };
  /** Homebrew updates; available when an installation was found (macOS and Linux, not in container mode). */
  brew?: { available: boolean; supported?: boolean };
  /** System Events; supported: false where there is no journal (macOS). */
  events?: { available: boolean; supported?: boolean };
  /** sensors: false where there are no hwmon temperature or fan readings (macOS). */
  metrics?: { sensors: boolean };
  files?: {
    /** Configured places ("0", "1"…) followed by mounted drives ("m-…") when showMounts is on. */
    roots: { id?: string | number; kind?: "place" | "drive"; name: string; path: string; hidden?: boolean }[];
    trash?: boolean;
    maxEditSize?: number;
    chunkSize?: number;
    showMounts?: boolean;
    /** Move/copy accept a destRoot different from root. */
    crossRoot?: boolean;
    /** Server-side bookmarks and hidden drives (GET /api/files/locations). */
    bookmarks?: boolean;
  };
  terminal?: { available: boolean; shell: string; tmux: boolean; sudo?: boolean; docker?: boolean; maxSessions?: number };
}

export function useCapabilities() {
  return useQuery({
    queryKey: ["capabilities"],
    queryFn: () => api.get<Capabilities>("/api/capabilities"),
    staleTime: 15_000,
    refetchInterval: 30_000,
  });
}
