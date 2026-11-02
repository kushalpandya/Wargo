// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { Fingerprint, KeyRound, LifeBuoy } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { getAssertion, webauthnSupported } from "@/lib/webauthn";
import { useAuthState } from "@/app/session";
import { Logo } from "@/app/Logo";
import { Button } from "@/components/ui/button";
import { FormField, Input } from "@/components/ui/form";
import { ErrorNote, Notice } from "@/components/ui/misc";

type Step = { kind: "password" } | { kind: "second"; token: string; recovery: boolean };

export function AuthShell({ children, title, subtitle }: { children: React.ReactNode; title: string; subtitle?: string }) {
  return (
    <div className="flex min-h-dvh items-center justify-center bg-bg px-4 py-10">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex items-center gap-2.5">
          <Logo className="size-8" />
          <div>
            <div className="text-base font-semibold leading-tight tracking-tight">Wargo</div>
            <div className="text-xs text-muted">Homelab control</div>
          </div>
        </div>
        <div className="rounded-lg border border-border bg-surface p-5 shadow-[var(--shadow)]">
          <h1 className="text-[0.9375rem] font-semibold">{title}</h1>
          {subtitle && <p className="mt-0.5 text-sm text-muted">{subtitle}</p>}
          <div className="mt-4">{children}</div>
        </div>
      </div>
    </div>
  );
}

export function Login() {
  const { data: state } = useAuthState();
  const nav = useNavigate();
  const qc = useQueryClient();
  const [step, setStep] = useState<Step>({ kind: "password" });
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  // The password was right but this account must finish with a passkey
  // (it has passkeys and no TOTP / recovery codes left).
  const [passkeyRequired, setPasskeyRequired] = useState(false);

  const done = async (next: string) => {
    // Nothing from a previous session should survive into this one.
    qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "auth" });
    await qc.invalidateQueries({ queryKey: ["auth"] });
    nav(next === "totp-setup" ? "/setup/2fa" : "/", { replace: true });
  };

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  const submitPassword = (e: React.FormEvent) => {
    e.preventDefault();
    run(async () => {
      let r: { next: string; token?: string };
      try {
        r = await api.post<{ next: string; token?: string }>("/api/auth/login", { username, password });
      } catch (e) {
        if (e instanceof ApiError && e.code === "passkey_required") {
          setPassword("");
          setPasskeyRequired(true);
          return;
        }
        throw e;
      }
      setPassword("");
      if (r.next === "second-factor" && r.token) setStep({ kind: "second", token: r.token, recovery: false });
      else await done(r.next);
    });
  };

  const submitSecond = (e: React.FormEvent) => {
    e.preventDefault();
    if (step.kind !== "second") return;
    run(async () => {
      const body = step.recovery ? { token: step.token, recovery: code } : { token: step.token, code };
      const r = await api.post<{ next: string }>("/api/auth/second-factor", body);
      await done(r.next);
    });
  };

  const passkey = () =>
    run(async () => {
      const b = await api.post<{ ceremony: string; options: Record<string, unknown> }>("/api/auth/passkey/login/begin");
      const assertion = await getAssertion(b.options);
      const r = await api.request<{ next: string }>(`/api/auth/passkey/login/finish?ceremony=${encodeURIComponent(b.ceremony)}`, {
        method: "POST",
        body: assertion,
      });
      await done(r.next);
    });

  const canPasskey = (!!state?.passkeys || passkeyRequired) && webauthnSupported();
  const errMsg = error instanceof ApiError || error instanceof Error ? error : null;

  if (step.kind === "second") {
    return (
      <AuthShell
        title="Two-factor verification"
        subtitle={step.recovery ? "Enter one of your recovery codes." : "Enter the 6-digit code from your authenticator app."}
      >
        <form onSubmit={submitSecond} className="flex flex-col gap-3">
          <FormField label={step.recovery ? "Recovery code" : "Authentication code"} htmlFor="code">
            <Input
              id="code"
              autoFocus
              inputMode={step.recovery ? "text" : "numeric"}
              autoComplete="one-time-code"
              pattern={step.recovery ? undefined : "[0-9 ]*"}
              placeholder={step.recovery ? "xxxx-xxxx-xxxx" : "123 456"}
              className="num text-center text-base tracking-[0.2em]"
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
          </FormField>
          {errMsg && <ErrorNote error={errMsg} />}
          <Button type="submit" variant="primary" loading={busy} disabled={!code}>
            Verify
          </Button>
          <button
            type="button"
            className="inline-flex items-center justify-center gap-1.5 text-xs text-muted hover:text-fg"
            onClick={() => {
              setCode("");
              setStep({ ...step, recovery: !step.recovery });
            }}
          >
            <LifeBuoy className="size-3.5" />
            {step.recovery ? "Use authenticator code" : "Use a recovery code"}
          </button>
        </form>
      </AuthShell>
    );
  }

  return (
    <AuthShell title="Sign in" subtitle="Use your host account on this server.">
      {passkeyRequired && (
        <Notice tone="warn" className="mb-4">
          {webauthnSupported()
            ? "This account signs in with a passkey. Use “Sign in with passkey” below to continue."
            : "This account signs in with a passkey, but passkeys need HTTPS and a supported browser. Open Wargo over HTTPS to sign in."}
        </Notice>
      )}
      {canPasskey && (
        <>
          <Button variant="primary" className="w-full" icon={<Fingerprint />} loading={busy} onClick={passkey}>
            Sign in with passkey
          </Button>
          <div className="my-4 flex items-center gap-3 text-2xs uppercase tracking-wider text-subtle">
            <span className="h-px flex-1 bg-border" /> or password <span className="h-px flex-1 bg-border" />
          </div>
        </>
      )}
      <form onSubmit={submitPassword} className="flex flex-col gap-3">
        <FormField label="Username" htmlFor="username">
          <Input id="username" autoComplete="username" autoCapitalize="off" spellCheck={false} autoFocus value={username} onChange={(e) => setUsername(e.target.value)} />
        </FormField>
        <FormField label="Password" htmlFor="password">
          <Input id="password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </FormField>
        {errMsg && <ErrorNote error={errMsg} />}
        <Button type="submit" variant={canPasskey ? "secondary" : "primary"} icon={<KeyRound />} loading={busy} disabled={!username || !password}>
          Continue
        </Button>
      </form>
      {state && !state.secure && (
        <Notice tone="warn" className="mt-4">
          This connection isn't HTTPS. Passkeys are unavailable and your password travels unencrypted — use a reverse proxy with TLS.
        </Notice>
      )}
      {state && !state.pam && (
        <Notice tone="crit" className="mt-4">
          This build has no PAM support, so password sign-in is unavailable.
        </Notice>
      )}
    </AuthShell>
  );
}
