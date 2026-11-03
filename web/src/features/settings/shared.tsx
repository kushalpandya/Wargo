// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState, type ReactNode } from "react";
import type { UseMutationResult } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { FormField, Input } from "@/components/ui/form";
import { ErrorNote } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { useMe } from "@/app/session";
import { copyText } from "@/lib/clipboard";

/**
 * Cards of a settings section in balanced columns (like masonry): each card
 * keeps its own height and the next one starts right below it, so cards of
 * different heights never leave gaps. Columns are at least 30rem and the
 * whole grid is capped so a card never grows past ~42rem: one column on
 * laptops, two or three on wide screens. Give a child "settings-wide" to
 * span every column.
 */
export function SettingsGrid({ children }: { children: ReactNode }) {
  return <div className="settings-grid">{children}</div>;
}

/** Cards of a settings section, one per row, at a comfortable reading width. */
export function SettingsStack({ children }: { children: ReactNode }) {
  return <div className="settings-stack">{children}</div>;
}

/** SettingRows (or other items with the same padding) separated by lines. */
export function SettingsList({ children }: { children: ReactNode }) {
  return <div className="divide-y divide-border/70">{children}</div>;
}

/**
 * A settings object saved whole on every change. Returns what to show (the
 * save in flight, else the server's copy) and an updater that builds on it,
 * so quick successive changes don't undo each other.
 */
export function usePatchSave<T extends object>(data: T | undefined, save: UseMutationResult<T, Error, T>) {
  const value = save.isPending && save.variables ? save.variables : data;
  const update = (patch: Partial<T>) => {
    if (value) save.mutate({ ...value, ...patch }, { onError: (e) => toast.error("Could not save", e) });
  };
  return [value, update] as const;
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
