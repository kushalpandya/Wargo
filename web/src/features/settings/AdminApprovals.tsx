// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BadgeCheck, Fingerprint, KeyRound, ShieldAlert, Terminal } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Checkbox } from "@/components/ui/form";
import { Badge, ErrorNote, Notice, Skeleton, type Tone } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { CodeBlock, SettingRow } from "./shared";
import { usePasskeys } from "./hooks";

interface Methods {
  sudo: boolean;
  passkey: boolean;
  noNewPrivsHint?: boolean;
}

interface PrivdStatus {
  socket: string;
  installed: boolean;
  reachable: boolean;
  credentials: number;
  error?: string;
  executable: string;
  uid: number;
}

function TrustDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const { data: passkeys } = usePasskeys();
  const [sel, setSel] = useState<Set<string>>(new Set());
  useEffect(() => {
    if (open && passkeys) setSel(new Set(passkeys.map((p) => p.id)));
  }, [open, passkeys]);
  const trust = useMutation({
    mutationFn: () => api.post<{ trusted: number }>("/api/elevate/privd/trust", { passkeyIds: [...sel] }),
    onSuccess: (r) => {
      toast.ok(`wargo-privd now trusts ${r.trusted} passkey${r.trusted === 1 ? "" : "s"}`);
      qc.invalidateQueries({ queryKey: ["elevate"] });
      qc.invalidateQueries({ queryKey: ["capabilities"] });
      onOpenChange(false);
    },
  });
  const canceled = trust.error instanceof ApiError && trust.error.code === "elevation_canceled";
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Trust passkeys for admin approvals"
      description="wargo-privd will accept these passkeys to approve privileged actions. This replaces the currently trusted set and needs your sudo password once."
      size="md"
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" icon={<BadgeCheck />} loading={trust.isPending} disabled={sel.size === 0} onClick={() => trust.mutate()}>
            Trust {sel.size} passkey{sel.size === 1 ? "" : "s"}
          </Button>
        </>
      }
    >
      {!passkeys?.length ? (
        <Notice tone="warn">Add a passkey first (Passkeys section above).</Notice>
      ) : (
        <ul className="divide-y divide-border rounded-md border border-border">
          {passkeys.map((p) => (
            <li key={p.id} className="flex items-center gap-3 px-3 py-2.5">
              <Checkbox
                checked={sel.has(p.id)}
                aria-label={`Trust ${p.name}`}
                onCheckedChange={(b) =>
                  setSel((s) => {
                    const n = new Set(s);
                    if (b) n.add(p.id);
                    else n.delete(p.id);
                    return n;
                  })
                }
              />
              <KeyRound className="size-4 shrink-0 text-muted" aria-hidden="true" />
              <span className="min-w-0 flex-1 truncate text-sm font-medium">{p.name}</span>
              <span className="num text-xs text-muted">{p.rpId}</span>
            </li>
          ))}
        </ul>
      )}
      {trust.error && !canceled && <ErrorNote error={trust.error} className="mt-3" />}
    </Dialog>
  );
}

function SetupInstructions({ st }: { st: PrivdStatus }) {
  const origin = location.origin;
  const wargoBin = st.executable && st.executable.startsWith("/usr/") ? st.executable : "/usr/local/bin/wargo";
  const config = `allowed_uid = ${st.uid}
origins = ["${origin}"]
wargo_binary = "${wargoBin}"
credentials = "/var/lib/wargo-privd/credentials.json"`;
  return (
    <div className="flex flex-col gap-3 text-sm">
      <p className="text-muted">
        <span className="font-medium text-fg">wargo-privd</span> is a small optional root service. It keeps your passkeys' public keys and verifies every approval
        itself, so even a compromised Wargo process cannot run privileged commands without your authenticator. Run these on the server:
      </p>
      <ol className="flex flex-col gap-3">
        <li className="flex flex-col gap-1.5">
          <span className="text-xs font-medium text-muted">1. Install the helper (built with <span className="num">make privd</span> or from the release archive)</span>
          <CodeBlock label="install command">{`sudo install -m 0755 -o root -g root bin/wargo-privd /usr/local/bin/wargo-privd`}</CodeBlock>
        </li>
        <li className="flex flex-col gap-1.5">
          <span className="text-xs font-medium text-muted">2. Configure it (list every HTTPS origin you use for Wargo)</span>
          <CodeBlock label="config command">{`sudo install -d -m 0755 /etc/wargo
sudo tee /etc/wargo/privd.toml >/dev/null <<'EOF'
${config}
EOF`}</CodeBlock>
        </li>
        <li className="flex flex-col gap-1.5">
          <span className="text-xs font-medium text-muted">3. Start the service</span>
          <CodeBlock label="systemd commands">{`sudo install -m 0644 deploy/systemd/wargo-privd.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now wargo-privd`}</CodeBlock>
        </li>
        <li className="text-xs text-muted">4. Come back here and choose “Trust passkeys”.</li>
      </ol>
      {wargoBin !== st.executable && (
        <Notice tone="frost">
          This Wargo runs from <span className="num">{st.executable}</span>. privd only runs a root-owned wargo binary, so install Wargo to{" "}
          <span className="num">{wargoBin}</span> (owned by root) for helper operations to work with passkey approvals.
        </Notice>
      )}
    </div>
  );
}

export function AdminApprovals() {
  const { data: methods, isLoading: ml } = useQuery({ queryKey: ["elevate", "methods"], queryFn: () => api.get<Methods>("/api/elevate/methods") });
  const { data: st, isLoading: sl } = useQuery({ queryKey: ["elevate", "privd"], queryFn: () => api.get<PrivdStatus>("/api/elevate/privd"), refetchInterval: 30_000 });
  const { data: passkeys } = usePasskeys();
  const [trusting, setTrusting] = useState(false);

  let privdTone: Tone = "neutral";
  let privdLabel = "Not installed";
  let privdDesc = "Approve admin actions with a passkey instead of typing your sudo password.";
  if (st?.reachable && st.credentials > 0) {
    privdTone = "ok";
    privdLabel = "Active";
    privdDesc = `wargo-privd is running and trusts ${st.credentials} passkey${st.credentials === 1 ? "" : "s"}.`;
  } else if (st?.reachable) {
    privdTone = "warn";
    privdLabel = "No passkeys";
    privdDesc = "wargo-privd is running but trusts no passkeys yet.";
  } else if (st?.installed) {
    privdTone = "crit";
    privdLabel = "Unreachable";
    privdDesc = `The socket ${st.socket} exists but wargo-privd did not answer${st.error ? `: ${st.error}` : "."}`;
  }

  return (
    <Card>
      <CardHeader title="Admin approvals" icon={<ShieldAlert />} />
      <CardBody className="flex flex-col gap-3">
        <p className="text-sm text-muted">
          Wargo never keeps root. Privileged actions — signalling other users' processes, system updates, reboot — ask for approval every time.
        </p>
        {ml || sl ? (
          <Skeleton className="h-24" />
        ) : (
          <div className="divide-y divide-border">
            <SettingRow
              title={
                <span className="flex items-center gap-2">
                  <Terminal className="size-4 text-muted" aria-hidden="true" /> sudo password
                </span>
              }
              description={
                methods?.sudo
                  ? "Your password is passed to sudo for one command and never stored."
                  : methods?.noNewPrivsHint
                    ? "Blocked: the Wargo service runs with NoNewPrivileges, so sudo cannot work."
                    : "sudo is not installed on this host."
              }
            >
              <Badge tone={methods?.sudo ? "ok" : "crit"}>{methods?.sudo ? "Available" : "Unavailable"}</Badge>
            </SettingRow>
            <SettingRow
              title={
                <span className="flex items-center gap-2">
                  <Fingerprint className="size-4 text-muted" aria-hidden="true" /> Passkey via wargo-privd
                </span>
              }
              description={privdDesc}
            >
              <Badge tone={privdTone}>{privdLabel}</Badge>
              {st?.reachable && (
                <Button size="sm" icon={<BadgeCheck />} disabled={!passkeys?.length} onClick={() => setTrusting(true)}>
                  {st.credentials > 0 ? "Change trusted passkeys" : "Trust passkeys"}
                </Button>
              )}
            </SettingRow>
          </div>
        )}
        {st && !(st.reachable && st.credentials > 0) && (
          <details className="group rounded-md border border-border bg-surface-2/50 open:bg-transparent" open={!st.reachable}>
            <summary className="cursor-pointer select-none rounded-md px-3 py-2 text-sm font-medium hover:bg-surface-2">
              {st.reachable ? "How passkey approvals work" : "Set up passkey approvals"}
            </summary>
            <div className="border-t border-border px-3 py-3">
              {st.reachable ? (
                <p className="text-sm text-muted">
                  Choose “Trust passkeys” and pick which of your passkeys may approve admin actions. Wargo asks for your sudo password once to hand their public keys to
                  wargo-privd; after that, approvals only need your passkey. sudo remains available as a fallback.
                </p>
              ) : (
                <SetupInstructions st={st} />
              )}
            </div>
          </details>
        )}
      </CardBody>
      <TrustDialog open={trusting} onOpenChange={setTrusting} />
    </Card>
  );
}
