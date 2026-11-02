// SPDX-License-Identifier: AGPL-3.0-only
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";

export interface AuthState {
  authenticated: boolean;
  dev: boolean;
  pam: boolean;
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
  return useQuery({ queryKey: ["auth", "state"], queryFn: () => api.get<AuthState>("/api/auth/state"), staleTime: 30_000 });
}

export function useMe(enabled = true) {
  return useQuery({ queryKey: ["auth", "me"], queryFn: () => api.get<Me>("/api/auth/me"), enabled, staleTime: 60_000 });
}

export interface Capabilities {
  version: string;
  elevation: { sudo: boolean; passkey: boolean; noNewPrivsHint?: boolean };
  docker?: { available: boolean; error?: string; casaos?: boolean };
  prowlarr?: { available: boolean; configured: boolean; hasTransmissionClient: boolean; version?: string; error?: string };
  transmission?: { available: boolean; configured: boolean; version?: string; error?: string };
  launcher?: { enabled: boolean; placement: "overview" | "page" };
  pkg?: { backend: string; available: boolean; distro: string };
  files?: { roots: { name: string; path: string }[] };
  terminal?: { available: boolean; shell: string; tmux: boolean };
}

export function useCapabilities() {
  return useQuery({
    queryKey: ["capabilities"],
    queryFn: () => api.get<Capabilities>("/api/capabilities"),
    staleTime: 15_000,
    refetchInterval: 30_000,
  });
}
