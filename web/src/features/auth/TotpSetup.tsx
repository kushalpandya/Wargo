// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Copy, Download } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { FormField, Input } from "@/components/ui/form";
import { ErrorNote, Notice, Spinner } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { AuthShell } from "./Login";

interface Setup {
  secret: string;
  url: string;
  qr: string;
}

/** Enrolment panel, used by the forced first-login flow and Settings. */
export function TotpEnroll({ onDone }: { onDone: (codes: string[]) => void }) {
  const [setup, setSetup] = useState<Setup | null>(null);
  const [code, setCode] = useState("");
  const begin = useMutation({ mutationFn: () => api.post<Setup>("/api/auth/totp/setup"), onSuccess: setSetup });
  const enable = useMutation({
    mutationFn: () => api.post<{ recoveryCodes: string[] }>("/api/auth/totp/enable", { code }),
    onSuccess: (r) => onDone(r.recoveryCodes),
  });
  // Guard against double invocation (React StrictMode): each setup call
  // generates a new secret, so only the first may run.
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    begin.mutate();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (begin.isPending || !setup) return begin.error ? <ErrorNote error={begin.error} /> : <Spinner />;
  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        enable.mutate();
      }}
    >
      <ol className="list-decimal space-y-1 pl-4 text-sm text-muted">
        <li>Scan this QR code with an authenticator app.</li>
        <li>Enter the 6-digit code it shows.</li>
      </ol>
      <div className="flex justify-center rounded-md border border-border bg-white p-3">
        <img src={setup.qr} alt="TOTP QR code" width={180} height={180} />
      </div>
      <details className="text-xs text-muted">
        <summary className="cursor-pointer">Can't scan? Enter the key manually</summary>
        <code className="mt-1.5 block break-all rounded bg-surface-2 p-2 text-fg">{setup.secret}</code>
      </details>
      <FormField label="Code" htmlFor="totp-code">
        <Input id="totp-code" inputMode="numeric" autoComplete="one-time-code" className="num text-center tracking-[0.2em]" value={code} onChange={(e) => setCode(e.target.value)} />
      </FormField>
      {enable.error && <ErrorNote error={enable.error} />}
      <Button type="submit" variant="primary" loading={enable.isPending} disabled={code.replace(/\s/g, "").length < 6}>
        Enable two-factor
      </Button>
    </form>
  );
}

export function RecoveryCodes({ codes, onContinue }: { codes: string[]; onContinue: () => void }) {
  const text = codes.join("\n");
  return (
    <div className="flex flex-col gap-3">
      <Notice tone="warn">Save these recovery codes somewhere safe. Each works once if you lose your authenticator. They won't be shown again.</Notice>
      <div className="num grid grid-cols-2 gap-1.5 rounded-md border border-border bg-surface-2 p-3 text-[0.8125rem]">
        {codes.map((c) => (
          <span key={c}>{c}</span>
        ))}
      </div>
      <div className="flex gap-2">
        <Button
          size="sm"
          icon={<Copy />}
          onClick={async () => {
            await navigator.clipboard?.writeText(text);
            toast.ok("Recovery codes copied");
          }}
        >
          Copy
        </Button>
        <Button
          size="sm"
          icon={<Download />}
          onClick={() => {
            const a = document.createElement("a");
            a.href = URL.createObjectURL(new Blob([text + "\n"], { type: "text/plain" }));
            a.download = "wargo-recovery-codes.txt";
            a.click();
            URL.revokeObjectURL(a.href);
          }}
        >
          Download
        </Button>
        <Button variant="primary" size="sm" className="ml-auto" onClick={onContinue}>
          I've saved them
        </Button>
      </div>
    </div>
  );
}

export function TotpSetupPage() {
  const nav = useNavigate();
  const qc = useQueryClient();
  const [codes, setCodes] = useState<string[] | null>(null);
  return (
    <AuthShell
      title={codes ? "Recovery codes" : "Set up two-factor authentication"}
      subtitle={codes ? undefined : "Wargo can control this server, so a second factor is required."}
    >
      {codes ? (
        <RecoveryCodes
          codes={codes}
          onContinue={async () => {
            await qc.invalidateQueries({ queryKey: ["auth"] });
            nav("/", { replace: true });
          }}
        />
      ) : (
        <TotpEnroll onDone={setCodes} />
      )}
    </AuthShell>
  );
}
