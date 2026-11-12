// SPDX-License-Identifier: AGPL-3.0-only
// The admin-approval dialog itself; loaded on first use by <ElevationProvider/>.
import { useEffect, useRef, useState } from "react";
import { Fingerprint, KeyRound, ShieldAlert } from "lucide-react";
import { api, type ElevationCredential } from "@/lib/api";
import { getAssertion, webauthnSupported } from "@/lib/webauthn";
import { Dialog } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input, FormField } from "@/components/ui/form";
import { ErrorNote, Notice } from "@/components/ui/misc";
import type { Pending } from "./Elevation";

const actionLabels: Record<string, string> = {
  "procs.signal": "Signal a process owned by another user",
  "files.op": "Change files you don't have permission for",
  "pkg.refresh": "Refresh package databases",
  "pkg.upgrade": "Install system updates",
  "brew.upgrade": "Install Homebrew updates",
  "system.power": "Power control",
  "containers.compose_update": "Update a compose project",
  "privd.trust": "Trust a passkey for admin approvals",
};

/**
 * Renders the sudo/passkey approval dialog whenever an API call answers
 * `elevation_required`. The password lives only in component state for the
 * duration of one request.
 */
export default function ElevationDialog({ pending, onDone }: { pending: Pending | null; onDone: () => void }) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  // Fresh state for every prompt (a retry after a wrong password shows its error).
  useEffect(() => {
    setPassword("");
    setErr(pending?.req.error ?? null);
    setBusy(false);
  }, [pending]);

  const finish = (c: ElevationCredential | null) => {
    pending?.resolve(c);
    onDone();
    setPassword("");
  };

  const usePasskey = async () => {
    if (!pending) return;
    setBusy(true);
    setErr(null);
    try {
      const ch = await api.post<{ challenge: string; options: Record<string, unknown> }>("/api/elevate/challenge", {
        action: pending.req.action,
        target: pending.req.target,
      });
      const assertion = await getAssertion(ch.options);
      finish({ method: "passkey", challenge: ch.challenge, assertion });
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  const req = pending?.req;
  const passkey = !!req?.methods?.passkey && webauthnSupported();
  // Container mode has no sudo: the account password approves instead.
  const container = req?.methods?.mode === "container";
  const sudo = container ? req?.methods?.password !== false : req?.methods?.sudo !== false;

  return (
    <Dialog
      open={!!pending}
      onOpenChange={(o) => !o && finish(null)}
      size="sm"
      title={
        <span className="flex items-center gap-2">
          <ShieldAlert className="size-4 text-accent" /> Administrator approval
        </span>
      }
      description={req ? actionLabels[req.action] ?? req.action : undefined}
    >
      {req && (
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (password) finish({ method: container ? "password" : "sudo", password });
          }}
        >
          {req.target && (
            <div className="num truncate rounded-md border border-border bg-surface-2 px-2.5 py-1.5 text-xs text-muted" title={req.target}>
              {req.target}
            </div>
          )}
          {err && <ErrorNote error={err} />}
          {passkey && (
            <Button variant="primary" icon={<Fingerprint />} loading={busy} onClick={usePasskey}>
              Approve with passkey
            </Button>
          )}
          {sudo ? (
            <>
              {passkey && <div className="text-center text-xs text-subtle">or</div>}
              <FormField label={container ? "Your password" : "sudo password"} htmlFor="elev-pw" hint="Used once for this action and never stored.">
                <Input
                  id="elev-pw"
                  ref={inputRef}
                  type="password"
                  autoComplete="current-password"
                  autoFocus={!passkey}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </FormField>
              <div className="flex justify-end gap-2">
                <Button onClick={() => finish(null)}>Cancel</Button>
                <Button type="submit" variant={passkey ? "secondary" : "primary"} icon={<KeyRound />} disabled={!password}>
                  Approve
                </Button>
              </div>
            </>
          ) : (
            !passkey && (
              <Notice tone="warn">
                No elevation method is available. {req.methods?.noNewPrivsHint ? "sudo is blocked by the service's NoNewPrivileges setting." : "Install sudo or set up wargo-privd."}
              </Notice>
            )
          )}
        </form>
      )}
    </Dialog>
  );
}
