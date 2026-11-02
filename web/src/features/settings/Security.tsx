// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fingerprint, KeyRound, LifeBuoy, Lock, MonitorSmartphone, Plus, ShieldCheck, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { ago, datetime } from "@/lib/format";
import { createCredential, webauthnSupported } from "@/lib/webauthn";
import { useMe } from "@/app/session";
import { Button, IconButton } from "@/components/ui/button";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { ConfirmDialog, Dialog } from "@/components/ui/dialog";
import { FormField, Input } from "@/components/ui/form";
import { Badge, EmptyState, ErrorNote, Notice, Skeleton } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { RecoveryCodes, TotpEnroll } from "@/features/auth/TotpSetup";
import { PasswordDialog, SettingRow, SettingsHeader } from "./shared";
import { AdminApprovals } from "./AdminApprovals";
import { usePasskeys, type Passkey } from "./hooks";

interface SessionRow {
  id: string;
  createdAt: string;
  lastSeen: string;
  ip: string;
  ua: string;
  method: string;
  current: boolean;
}

const methodLabels: Record<string, string> = {
  password: "Password",
  "password+totp": "Password + code",
  passkey: "Passkey",
  "password+recovery": "Recovery code",
  dev: "Dev bypass",
};

/** Very small UA summariser: "Firefox on Linux". */
function describeUA(ua: string): string {
  if (!ua) return "Unknown device";
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /OPR\//.test(ua)
      ? "Opera"
      : /Firefox\//.test(ua)
        ? "Firefox"
        : /Chrome\//.test(ua)
          ? "Chrome"
          : /Safari\//.test(ua)
            ? "Safari"
            : /curl\//.test(ua)
              ? "curl"
              : "Browser";
  const os = /Android/.test(ua)
    ? "Android"
    : /iPhone|iPad|iOS/.test(ua)
      ? "iOS"
      : /Mac OS X|Macintosh/.test(ua)
        ? "macOS"
        : /Windows/.test(ua)
          ? "Windows"
          : /Linux|X11/.test(ua)
            ? "Linux"
            : "";
  return os ? `${browser} on ${os}` : browser;
}

function TwoFactorCard() {
  const qc = useQueryClient();
  const { data: me } = useMe();
  const [enroll, setEnroll] = useState(false);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [disable, setDisable] = useState(false);
  const [regen, setRegen] = useState(false);
  const refresh = () => qc.invalidateQueries({ queryKey: ["auth"] });
  const enabled = !!me?.totpEnabled;
  const left = me?.recoveryCodesLeft ?? 0;

  return (
    <Card>
      <CardHeader
        title="Two-factor authentication"
        icon={<ShieldCheck />}
        actions={me ? <Badge tone={enabled ? "ok" : "warn"}>{enabled ? "On" : "Off"}</Badge> : undefined}
      />
      <CardBody className="divide-y divide-border">
        <SettingRow
          title="Authenticator app"
          description={
            enabled
              ? "A 6-digit code from your authenticator app is required after your password."
              : "Add a time-based code (TOTP) from an app like Aegis, 2FAS or 1Password."
          }
        >
          {enabled ? (
            <Button variant="outline-danger" size="sm" onClick={() => setDisable(true)}>
              Turn off
            </Button>
          ) : (
            <Button variant="primary" size="sm" onClick={() => setEnroll(true)}>
              Set up
            </Button>
          )}
        </SettingRow>
        <SettingRow
          title="Recovery codes"
          description={
            enabled ? (
              <>
                <span className={left <= 2 ? "font-medium text-warn" : undefined}>
                  {left} of 10 unused
                </span>
                {" · "}Single-use codes for when you lose your authenticator.
              </>
            ) : (
              "Created when you turn on two-factor authentication."
            )
          }
        >
          <Button size="sm" icon={<LifeBuoy />} disabled={!enabled} onClick={() => setRegen(true)}>
            Regenerate
          </Button>
        </SettingRow>
      </CardBody>

      <Dialog
        open={enroll}
        onOpenChange={(o) => {
          setEnroll(o);
          if (!o) {
            setCodes(null);
            refresh();
          }
        }}
        title={codes ? "Save your recovery codes" : "Set up two-factor authentication"}
        size="sm"
      >
        {enroll &&
          (codes ? (
            <RecoveryCodes
              codes={codes}
              onContinue={() => {
                setEnroll(false);
                setCodes(null);
                refresh();
                toast.ok("Two-factor authentication is on");
              }}
            />
          ) : (
            <TotpEnroll onDone={setCodes} />
          ))}
      </Dialog>

      <PasswordDialog
        open={disable}
        onOpenChange={setDisable}
        title="Turn off two-factor?"
        description="Signing in with a password will no longer ask for a code. Recovery codes stop working."
        confirmLabel="Turn off"
        danger
        onSubmit={async (password) => {
          await api.post("/api/auth/totp/disable", { password });
          refresh();
          toast.ok("Two-factor authentication turned off");
        }}
      />

      <PasswordDialog
        open={regen}
        onOpenChange={setRegen}
        title="Regenerate recovery codes?"
        description="All existing recovery codes stop working immediately."
        confirmLabel="Regenerate"
        onSubmit={async (password) => {
          const r = await api.post<{ recoveryCodes: string[] }>("/api/auth/recovery-codes", { password });
          refresh();
          setCodes(r.recoveryCodes);
        }}
      />
      <Dialog open={!!codes && !enroll} onOpenChange={(o) => !o && setCodes(null)} title="New recovery codes" size="sm">
        {codes && !enroll && <RecoveryCodes codes={codes} onContinue={() => setCodes(null)} />}
      </Dialog>
    </Card>
  );
}

function AddPasskeyDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const add = useMutation({
    mutationFn: async () => {
      const begin = await api.post<{ ceremony: string; options: Record<string, unknown> }>("/api/auth/passkeys/register/begin");
      const cred = await createCredential(begin.options);
      const q = new URLSearchParams({ ceremony: begin.ceremony, name: name.trim() || "Passkey" });
      return api.request(`/api/auth/passkeys/register/finish?${q}`, { method: "POST", body: cred });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["auth"] });
      toast.ok("Passkey added");
      setName("");
      onOpenChange(false);
    },
  });
  const errMsg =
    add.error instanceof DOMException && add.error.name === "NotAllowedError" ? "The passkey prompt was canceled or timed out." : add.error;
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) add.reset();
        onOpenChange(o);
      }}
      title="Add a passkey"
      description={`Passkeys are tied to this address (${location.hostname}). Add one for every hostname you sign in from.`}
      size="sm"
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" icon={<Fingerprint />} loading={add.isPending} onClick={() => add.mutate()}>
            Continue
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          add.mutate();
        }}
      >
        <FormField label="Name" htmlFor="pk-name" hint="So you can tell your passkeys apart, e.g. “YubiKey 5C” or “Pixel 8”.">
          <Input id="pk-name" value={name} maxLength={64} autoFocus placeholder="Passkey" onChange={(e) => setName(e.target.value)} />
        </FormField>
        {add.error ? <ErrorNote error={errMsg} /> : null}
      </form>
    </Dialog>
  );
}

function PasskeysCard() {
  const qc = useQueryClient();
  const { data, isLoading, error } = usePasskeys();
  const [adding, setAdding] = useState(false);
  const [deleting, setDeleting] = useState<Passkey | null>(null);
  const supported = webauthnSupported();
  const secure = window.isSecureContext;

  return (
    <Card>
      <CardHeader
        title="Passkeys"
        icon={<Fingerprint />}
        meta={data ? `${data.length}` : undefined}
        actions={
          <Button size="sm" icon={<Plus />} disabled={!supported} onClick={() => setAdding(true)}>
            Add passkey
          </Button>
        }
      />
      <CardBody className="flex flex-col gap-3">
        <p className="text-sm text-muted">
          Sign in with your fingerprint, face or a security key — no password or code needed. Passkeys can also approve admin actions (see below).
        </p>
        {!secure ? (
          <Notice tone="warn" icon={<Lock />}>
            Passkeys need a secure connection. Open Wargo over <strong>HTTPS</strong> (for example through your reverse proxy or tunnel) or via{" "}
            <span className="num">http://localhost</span> to add or use passkeys.
          </Notice>
        ) : (
          !supported && <Notice tone="warn">This browser does not support passkeys.</Notice>
        )}
        {isLoading ? (
          <Skeleton className="h-14" />
        ) : error ? (
          <ErrorNote error={error} />
        ) : !data?.length ? (
          <div className="rounded-md border border-dashed border-border-strong">
            <EmptyState icon={<KeyRound />} title="No passkeys yet" className="py-6" />
          </div>
        ) : (
          <ul className="divide-y divide-border rounded-md border border-border">
            {data.map((p) => (
              <li key={p.id} className="flex items-center gap-3 px-3 py-2.5">
                <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-surface-3 text-muted">
                  <KeyRound className="size-4" />
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                    <span className="truncate text-sm font-medium">{p.name}</span>
                    <Badge tone={p.rpId === location.hostname ? "frost" : "neutral"} title="Relying party (hostname the passkey works on)">
                      <span className="num normal-case tracking-normal">{p.rpId}</span>
                    </Badge>
                  </div>
                  <div className="text-xs text-muted">
                    Added <time dateTime={p.createdAt} title={datetime(p.createdAt)}>{ago(p.createdAt)}</time>
                    {" · "}
                    {p.lastUsed ? (
                      <>
                        last used <time dateTime={p.lastUsed} title={datetime(p.lastUsed)}>{ago(p.lastUsed)}</time>
                      </>
                    ) : (
                      "never used"
                    )}
                  </div>
                </div>
                <IconButton label={`Delete passkey ${p.name}`} size="sm" className="hover:text-crit" onClick={() => setDeleting(p)}>
                  <Trash2 />
                </IconButton>
              </li>
            ))}
          </ul>
        )}
      </CardBody>
      <AddPasskeyDialog open={adding} onOpenChange={setAdding} />
      <PasswordDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete passkey “${deleting?.name ?? ""}”?`}
        description="You will no longer be able to sign in or approve actions with it. Remove it from your device's password manager too."
        confirmLabel="Delete passkey"
        danger
        onSubmit={async (password) => {
          if (!deleting) return;
          await api.del(`/api/auth/passkeys/${encodeURIComponent(deleting.id)}`, { password });
          qc.invalidateQueries({ queryKey: ["auth"] });
          toast.ok("Passkey deleted");
        }}
      />
    </Card>
  );
}

function SessionsCard() {
  const qc = useQueryClient();
  const { data, isLoading, error } = useQuery({ queryKey: ["auth", "sessions"], queryFn: () => api.get<SessionRow[]>("/api/auth/sessions") });
  const [confirm, setConfirm] = useState(false);
  const revoke = useMutation({
    mutationFn: () => api.post<{ revoked: number }>("/api/auth/sessions/revoke-others"),
    onSuccess: (r) => {
      toast.ok(`Signed out ${r.revoked} other session${r.revoked === 1 ? "" : "s"}`);
      setConfirm(false);
      qc.invalidateQueries({ queryKey: ["auth", "sessions"] });
    },
  });
  const others = (data ?? []).filter((s) => !s.current).length;

  return (
    <Card>
      <CardHeader
        title="Active sessions"
        icon={<MonitorSmartphone />}
        meta={data ? `${data.length}` : undefined}
        actions={
          <Button size="sm" variant="outline-danger" disabled={!others} onClick={() => setConfirm(true)}>
            Sign out others
          </Button>
        }
      />
      {isLoading ? (
        <CardBody>
          <Skeleton className="h-14" />
        </CardBody>
      ) : error ? (
        <CardBody>
          <ErrorNote error={error} />
        </CardBody>
      ) : !data?.length ? (
        <CardBody>
          <p className="text-sm text-muted">No stored sessions{" "}(dev bypass sessions are not stored).</p>
        </CardBody>
      ) : (
        <ul className="divide-y divide-border">
          {data.map((s) => (
            <li key={s.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3.5 py-2.5">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium" title={s.ua}>
                    {describeUA(s.ua)}
                  </span>
                  {s.current && <Badge tone="ok">This device</Badge>}
                  <Badge>{methodLabels[s.method] ?? s.method}</Badge>
                </div>
                <div className="text-xs text-muted">
                  <span className="num">{s.ip || "unknown IP"}</span> · signed in <span title={datetime(s.createdAt)}>{ago(s.createdAt)}</span> · active{" "}
                  <span title={datetime(s.lastSeen)}>{ago(s.lastSeen)}</span>
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title="Sign out all other sessions?"
        description={`${others} other session${others === 1 ? "" : "s"} will be signed out immediately. This device stays signed in.`}
        confirmLabel="Sign out others"
        danger
        loading={revoke.isPending}
        onConfirm={() => revoke.mutate()}
      >
        {revoke.error && <ErrorNote error={revoke.error} />}
      </ConfirmDialog>
    </Card>
  );
}

export default function Security() {
  const { data: me } = useMe();
  return (
    <>
      <SettingsHeader
        title="Security"
        description={
          me ? (
            <>
              Signed in as <span className="font-medium text-fg">{me.user}</span> (group <span className="num">{me.adminGroup}</span>) via{" "}
              {methodLabels[me.method] ?? me.method}.
            </>
          ) : undefined
        }
      />
      {me?.dev && (
        <Notice tone="warn" className="mb-4">
          Dev mode: authentication is bypassed and password prompts are not checked.
        </Notice>
      )}
      <div className="flex flex-col gap-4">
        <TwoFactorCard />
        <PasskeysCard />
        <AdminApprovals />
        <SessionsCard />
      </div>
    </>
  );
}
