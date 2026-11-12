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
import { Checkbox, FormField, Input } from "@/components/ui/form";
import { usePref } from "@/lib/prefs";
import { ErrorNote, Notice } from "@/components/ui/misc";

type Step = { kind: "password" } | { kind: "second"; token: string; recovery: boolean };

export function AuthShell({ children, title, subtitle }: { children: React.ReactNode; title: string; subtitle?: string }) {
  return (
    <div className="wargo-canvas wargo-auth flex min-h-dvh items-center justify-center px-4 py-10">
      <div className="w-full max-w-[25rem]">
        <div className="mb-7 flex flex-col items-center gap-3 text-center">
          <Logo className="size-14 drop-shadow-[0_8px_18px_oklch(0.55_0.15_280/0.35)]" />
          <div>
            <div className="text-2xl font-bold tracking-tight">Wargo</div>
            <div className="text-sm text-muted">Your homelab, at a glance</div>
          </div>
        </div>
        <div className="rounded-2xl border border-border bg-surface/90 p-6 shadow-pop backdrop-blur sm:p-7">
          <h1 className="text-xl font-bold tracking-tight">{title}</h1>
          {subtitle && <p className="mt-1 text-sm text-muted">{subtitle}</p>}
          <div className="mt-5">{children}</div>
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
  // "Keep me signed in", remembered for the next sign-in on this browser.
  const [rememberPref, setRememberPref] = usePref("wargo.login.remember", "no", ["no", "yes"] as const);
  const remember = rememberPref === "yes";

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
        r = await api.post<{ next: string; token?: string }>("/api/auth/login", { username, password, remember });
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
      const r = await api.request<{ next: string }>(`/api/auth/passkey/login/finish?ceremony=${encodeURIComponent(b.ceremony)}${remember ? "&remember=1" : ""}`, {
        method: "POST",
        body: assertion,
      });
      await done(r.next);
    });

  const canPasskey = (!!state?.passkeys || passkeyRequired) && webauthnSupported();
  const rememberBox = (
    <Checkbox
      className="min-h-10"
      checked={remember}
      onCheckedChange={(v) => setRememberPref(v ? "yes" : "no")}
      label="Keep me signed in on this device"
    />
  );
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
          <div className="mt-2">{rememberBox}</div>
          <div className="my-3 flex items-center gap-3 text-2xs uppercase tracking-wider text-subtle">
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
        {!canPasskey && rememberBox}
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
          {state.pamError || "This build has no PAM support, so password sign-in is unavailable."}
        </Notice>
      )}
    </AuthShell>
  );
}
