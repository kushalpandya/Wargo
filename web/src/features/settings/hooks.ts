// SPDX-License-Identifier: AGPL-3.0-only
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";

export interface Passkey {
  id: string;
  name: string;
  rpId: string;
  createdAt: string;
  lastUsed?: string;
}

export function usePasskeys() {
  return useQuery({ queryKey: ["auth", "passkeys"], queryFn: () => api.get<Passkey[]>("/api/auth/passkeys") });
}
