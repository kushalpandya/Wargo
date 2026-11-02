// SPDX-License-Identifier: AGPL-3.0-only
import { lazy, Suspense, useEffect, useState } from "react";
import { setElevationPrompt, type ElevationCredential, type ElevationRequest } from "@/lib/api";

export type Pending = { req: ElevationRequest & { error?: string }; resolve: (c: ElevationCredential | null) => void };

const ElevationDialog = lazy(() => import("./ElevationDialog"));

/**
 * Shows the sudo/passkey approval dialog whenever an API call answers
 * `elevation_required`. The dialog code loads on first use; the password
 * lives only in its state for the duration of one request.
 */
export function ElevationProvider() {
  const [pending, setPending] = useState<Pending | null>(null);
  const [used, setUsed] = useState(false);

  useEffect(() => {
    setElevationPrompt(
      (req) =>
        new Promise<ElevationCredential | null>((resolve) => {
          setUsed(true);
          setPending({ req, resolve });
        }),
    );
    return () => setElevationPrompt(null);
  }, []);

  if (!used) return null;
  return (
    <Suspense fallback={null}>
      <ElevationDialog pending={pending} onDone={() => setPending(null)} />
    </Suspense>
  );
}
