// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { FormField, Input } from "@/components/ui/form";
import { ErrorNote } from "@/components/ui/misc";
import { useMe } from "@/app/session";
import { copyText } from "@/lib/clipboard";

/** Heading for a settings page. */
export function SettingsHeader({ title, description, actions }: { title: string; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
      <div className="min-w-0">
        <h2 className="text-base font-semibold tracking-tight">{title}</h2>
        {description && <p className="mt-0.5 text-sm text-muted">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

/** Label/description on the left, control on the right (stacks on phones). */
export function SettingRow({ title, description, children, className }: { title: ReactNode; description?: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={"flex flex-col gap-2 py-3 first:pt-0 last:pb-0 sm:flex-row sm:items-center sm:justify-between sm:gap-6 " + (className ?? "")}>
      <div className="min-w-0">
        <div className="text-sm font-medium">{title}</div>
        {description && <div className="mt-0.5 text-xs text-muted">{description}</div>}
      </div>
      {children && <div className="flex shrink-0 flex-wrap items-center gap-2">{children}</div>}
    </div>
  );
}

/**
 * Asks for the account password before a sensitive change. `onSubmit`
 * receives the password and should throw on failure.
 */
export function PasswordDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = "Confirm",
  danger,
  onSubmit,
  children,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: string;
  description?: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
  onSubmit: (password: string) => Promise<unknown>;
  children?: ReactNode;
}) {
  const { data: me } = useMe();
  const [pw, setPw] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  // Reset on open and on close so the password never outlives the dialog.
  useEffect(() => {
    setPw("");
    setErr(null);
    setBusy(false);
  }, [open]);
  const can = !!pw || !!me?.dev;
  const submit = async () => {
    if (!can) return;
    setBusy(true);
    setErr(null);
    try {
      await onSubmit(pw);
      setPw("");
      onOpenChange(false);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      description={description}
      size="sm"
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant={danger ? "danger" : "primary"} loading={busy} disabled={!can} onClick={submit}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {children}
        <FormField
          label={`Password for ${me?.user ?? "your account"}`}
          htmlFor="reauth-pw"
          hint={me?.dev ? "Dev mode: the password is not checked." : "Your host account password."}
        >
          <Input id="reauth-pw" type="password" autoComplete="current-password" autoFocus value={pw} onChange={(e) => setPw(e.target.value)} />
        </FormField>
        {err ? <ErrorNote error={err} /> : null}
      </form>
    </Dialog>
  );
}

/** Copyable code block for shell commands. */
export function CodeBlock({ children, label }: { children: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  const ref = useRef<HTMLPreElement>(null);
  return (
    <div className="group relative">
      <pre ref={ref} tabIndex={0} role="region" aria-label={label ?? "Command"} className="num scroll-area overflow-x-auto rounded-md border border-border bg-surface-2 px-3 py-2.5 pr-16 text-xs leading-relaxed text-fg focus-visible:outline-2 focus-visible:outline-focus">
        <code>{children}</code>
      </pre>
      <Button
        size="xs"
        className="absolute right-1.5 top-1.5"
        aria-label={label ? `Copy ${label}` : "Copy to clipboard"}
        onClick={async () => {
          if (await copyText(children)) {
            setCopied(true);
            window.setTimeout(() => setCopied(false), 1500);
          } else if (ref.current) {
            // No clipboard access: select the text instead.
            window.getSelection()?.selectAllChildren(ref.current);
          }
        }}
      >
        {copied ? "Copied" : "Copy"}
      </Button>
    </div>
  );
}
