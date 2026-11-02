// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useId, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Eye, EyeOff, FileKey2, Radar, Unplug, XCircle } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Card, CardHeader } from "@/components/ui/card";
import { Button, IconButton } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { FormField, Input } from "@/components/ui/form";
import { Badge, Notice, Skeleton, Spinner } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { errorMessage, type Candidate, type Discovery, type IntegrationsStatus, type TestResult } from "./api";

const KEY = ["integrations"];

function useStatus() {
  return useQuery({
    queryKey: KEY,
    queryFn: () => api.get<IntegrationsStatus>("/api/integrations"),
    staleTime: 15_000,
  });
}

function useDiscover() {
  return useMutation({
    mutationFn: () => api.get<Discovery>("/api/integrations/discover"),
  });
}

function useInvalidate() {
  const qc = useQueryClient();
  return () => {
    qc.invalidateQueries({ queryKey: KEY });
    qc.invalidateQueries({ queryKey: ["capabilities"] });
  };
}

/** Prowlarr + Transmission connection settings (used by Settings → Integrations). */
export default function IntegrationSettings() {
  const status = useStatus();
  const discover = useDiscover();
  if (status.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-64" />
        <Skeleton className="h-56" />
      </div>
    );
  }
  if (status.error || !status.data) return <Notice tone="crit">{errorMessage(status.error)}</Notice>;
  return (
    <div className="space-y-4">
      <ProwlarrCard status={status.data} discover={discover} />
      <TransmissionCard status={status.data} discover={discover} />
    </div>
  );
}

type DiscoverMutation = ReturnType<typeof useDiscover>;

function StatusPill({ configured, available, version, error }: { configured: boolean; available: boolean; version?: string; error?: string }) {
  if (!configured) return <Badge>Not configured</Badge>;
  if (available)
    return (
      <Badge tone="ok" title={version}>
        Connected{version ? ` · ${version.split(" ")[0]}` : ""}
      </Badge>
    );
  return (
    <Badge tone="crit" title={error}>
      Unreachable
    </Badge>
  );
}

function SecretInput({
  id,
  value,
  onChange,
  placeholder,
  autoComplete,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  autoComplete: string;
}) {
  const [show, setShow] = useState(false);
  return (
    <div className="relative">
      <Input
        id={id}
        type={show ? "text" : "password"}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        autoComplete={autoComplete}
        spellCheck={false}
        className="num pr-9"
      />
      <IconButton label={show ? "Hide" : "Show"} size="sm" className="absolute right-0.5 top-1/2 -translate-y-1/2" onClick={() => setShow((s) => !s)}>
        {show ? <EyeOff /> : <Eye />}
      </IconButton>
    </div>
  );
}

function TestOutcome({ result, extra }: { result: TestResult | null; extra?: ReactNode }) {
  if (!result) return null;
  return (
    <div role="status" className={cn("flex min-w-0 items-start gap-1.5 text-xs", result.ok ? "text-ok" : "text-crit")}>
      {result.ok ? <CheckCircle2 className="mt-px size-3.5 shrink-0" /> : <XCircle className="mt-px size-3.5 shrink-0" />}
      <span className="min-w-0 break-words">
        {result.ok ? `Connected${result.version ? ` · version ${result.version.split(" ")[0]}` : ""}` : result.error}
        {result.ok && extra}
      </span>
    </div>
  );
}

function CandidateRow({ c, onUse, children }: { c: Candidate; onUse?: () => void; children?: ReactNode }) {
  return (
    <li className="flex flex-col gap-2 px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium">
            {c.container}
            <span className="num ml-2 text-xs font-normal text-subtle">{c.image}</span>
          </div>
          <div className="num truncate text-xs text-muted">
            {c.url ?? c.note}
            {c.networkVia && <span className="text-subtle"> · via {c.networkVia} network</span>}
          </div>
        </div>
        {c.url && onUse && (
          <Button size="sm" onClick={onUse}>
            Use URL
          </Button>
        )}
      </div>
      {children}
    </li>
  );
}

function DetectPanel({ discover, children, empty }: { discover: DiscoverMutation; children: ReactNode; empty: boolean }) {
  if (discover.isIdle) return null;
  return (
    <div className="rounded-md border border-border bg-surface-2">
      <div className="flex items-center gap-2 border-b border-border px-3 py-1.5 text-2xs font-semibold uppercase tracking-wider text-subtle">
        Detected containers
        {discover.isPending && <Spinner className="size-3" />}
      </div>
      {discover.error ? (
        <p className="px-3 py-2.5 text-xs text-crit">{errorMessage(discover.error)}</p>
      ) : discover.data && !discover.data.docker.available ? (
        <p className="px-3 py-2.5 text-xs text-muted">Docker is not reachable: {discover.data.docker.error}</p>
      ) : discover.data && empty ? (
        <p className="px-3 py-2.5 text-xs text-muted">No matching running container found.</p>
      ) : (
        <ul className="divide-y divide-border">{children}</ul>
      )}
    </div>
  );
}

function ProwlarrCard({ status, discover }: { status: IntegrationsStatus; discover: DiscoverMutation }) {
  const p = status.prowlarr;
  const invalidate = useInvalidate();
  const ids = { url: useId(), key: useId() };
  const [url, setUrl] = useState(p.url);
  const [apiKey, setApiKey] = useState("");
  const [test, setTest] = useState<TestResult | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);
  useEffect(() => setUrl(p.url), [p.url]);

  // Secrets are read from state inside mutationFn rather than passed as
  // mutation variables, which React Query would keep in its cache.
  const save = useMutation({
    mutationFn: (body: { url: string; withKey?: boolean }) => api.put("/api/integrations/prowlarr", { url: body.url, apiKey: body.withKey ? apiKey || undefined : undefined }),
    onSuccess: (_, body) => {
      setApiKey("");
      invalidate();
      toast.ok(body.url ? "Prowlarr settings saved" : "Prowlarr disconnected");
    },
    onError: (e) => toast.error("Could not save", errorMessage(e)),
  });
  const runTest = useMutation({
    mutationFn: () =>
      api.post<TestResult>("/api/integrations/prowlarr/test", {
        url,
        apiKey: apiKey || undefined,
      }),
    onSuccess: setTest,
    onError: (e) => setTest({ ok: false, error: errorMessage(e) }),
  });
  const importKey = useMutation({
    mutationFn: (c: Candidate) =>
      api.post("/api/integrations/prowlarr/import-key", {
        configPath: c.configPath,
        url: url || c.url,
      }),
    onSuccess: () => {
      invalidate();
      setTest(null);
      toast.ok("API key imported", "Prowlarr is now connected.");
    },
    onError: (e) => toast.error("Could not import API key", errorMessage(e)),
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ url, withKey: true });
  };
  const candidates = discover.data?.prowlarr ?? [];

  return (
    <Card>
      <CardHeader
        title="Prowlarr"
        meta="torrent search"
        actions={
          <>
            <StatusPill {...p} />
            <Button size="sm" variant="ghost" icon={<Radar />} loading={discover.isPending} onClick={() => discover.mutate()}>
              Detect
            </Button>
          </>
        }
      />
      <form onSubmit={submit} className="space-y-3.5 p-3.5">
        {p.configured && !p.available && p.error && <Notice tone="crit">{p.error}</Notice>}
        <DetectPanel discover={discover} empty={candidates.length === 0}>
          {candidates.map((c) => (
            <CandidateRow key={c.container} c={c} onUse={() => c.url && setUrl(c.url)}>
              {c.configPath && (
                <div className="flex flex-wrap items-center gap-2 rounded border border-border bg-surface px-2.5 py-2">
                  <FileKey2 className="size-4 shrink-0 text-subtle" />
                  <div className="min-w-0 flex-1 text-xs">
                    <div className="text-muted">{c.configReadable ? "API key can be read from" : "Not readable by Wargo:"}</div>
                    <div className="num break-all text-fg">{c.configPath}</div>
                  </div>
                  {c.configReadable && (
                    <Button size="sm" variant="primary" loading={importKey.isPending} onClick={() => importKey.mutate(c)}>
                      Import API key from config.xml
                    </Button>
                  )}
                </div>
              )}
            </CandidateRow>
          ))}
        </DetectPanel>

        <div className="grid gap-3.5 sm:grid-cols-2">
          <FormField label="URL" htmlFor={ids.url} hint="Base address, e.g. http://127.0.0.1:9696">
            <Input
              id={ids.url}
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="http://127.0.0.1:9696"
              className="num"
              inputMode="url"
              autoComplete="off"
              spellCheck={false}
            />
          </FormField>
          <FormField
            label="API key"
            htmlFor={ids.key}
            hint={p.hasApiKey ? "A key is saved (encrypted). Leave blank to keep it." : "Settings → General → Security in Prowlarr."}
          >
            <SecretInput
              id={ids.key}
              value={apiKey}
              onChange={setApiKey}
              placeholder={p.hasApiKey ? "•••••••••••••••• (saved)" : "Paste API key"}
              autoComplete="off"
            />
          </FormField>
        </div>

        {p.available && (
          <p className="text-xs text-muted">
            {p.hasTransmissionClient && p.transmissionClient ? (
              <>
                “Queue in Transmission” uses Prowlarr’s download client{" "}
                <span className="num text-fg">
                  {p.transmissionClient.host}:{p.transmissionClient.port}
                </span>
                .
              </>
            ) : (
              "Prowlarr has no enabled Transmission download client — queuing will use Wargo’s own Transmission connection below."
            )}
          </p>
        )}

        <div className="flex flex-wrap items-center gap-2 border-t border-border pt-3.5">
          <TestOutcome
            result={test}
            extra={test?.ok ? (test.hasTransmissionClient ? " · Transmission download client found" : " · no Transmission download client") : null}
          />
          <div className="ml-auto flex flex-wrap gap-2">
            {p.configured && (
              <Button variant="ghost" icon={<Unplug />} onClick={() => setConfirmClear(true)}>
                Disconnect
              </Button>
            )}
            <Button onClick={() => runTest.mutate()} loading={runTest.isPending} disabled={!url}>
              Test connection
            </Button>
            <Button type="submit" variant="primary" loading={save.isPending} disabled={!url || (!p.hasApiKey && !apiKey)}>
              Save
            </Button>
          </div>
        </div>
      </form>
      <ConfirmDialog
        open={confirmClear}
        onOpenChange={setConfirmClear}
        title="Disconnect Prowlarr?"
        description="The saved URL and API key are removed from Wargo. Prowlarr itself is not changed."
        confirmLabel="Disconnect"
        danger
        loading={save.isPending}
        onConfirm={() => save.mutate({ url: "" }, { onSuccess: () => setConfirmClear(false) })}
      />
    </Card>
  );
}

function TransmissionCard({ status, discover }: { status: IntegrationsStatus; discover: DiscoverMutation }) {
  const t = status.transmission;
  const invalidate = useInvalidate();
  const ids = { url: useId(), user: useId(), pass: useId() };
  const [url, setUrl] = useState(t.url);
  const [username, setUsername] = useState(t.username);
  const [password, setPassword] = useState("");
  const [test, setTest] = useState<TestResult | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);
  useEffect(() => setUrl(t.url), [t.url]);
  useEffect(() => setUsername(t.username), [t.username]);

  const save = useMutation({
    mutationFn: (body: { url: string; username?: string; withPassword?: boolean }) =>
      api.put("/api/integrations/transmission", { url: body.url, username: body.username, password: body.withPassword ? password || undefined : undefined }),
    onSuccess: (_, body) => {
      setPassword("");
      invalidate();
      toast.ok(body.url ? "Transmission settings saved" : "Transmission disconnected");
    },
    onError: (e) => toast.error("Could not save", errorMessage(e)),
  });
  const runTest = useMutation({
    mutationFn: () =>
      api.post<TestResult>("/api/integrations/transmission/test", {
        url,
        username,
        password: password || undefined,
      }),
    onSuccess: setTest,
    onError: (e) => setTest({ ok: false, error: errorMessage(e) }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ url, username, withPassword: true });
  };
  const candidates = discover.data?.transmission ?? [];
  const hint = discover.data?.transmissionFromProwlarr;
  const prowlarrHint = status.prowlarr.transmissionClient;

  return (
    <Card>
      <CardHeader
        title="Transmission"
        meta="downloads"
        actions={
          <>
            <StatusPill {...t} />
            <Button size="sm" variant="ghost" icon={<Radar />} loading={discover.isPending} onClick={() => discover.mutate()}>
              Detect
            </Button>
          </>
        }
      />
      <form onSubmit={submit} className="space-y-3.5 p-3.5">
        {t.configured && !t.available && t.error && <Notice tone="crit">{t.error}</Notice>}
        <DetectPanel discover={discover} empty={candidates.length === 0 && !hint}>
          {candidates.map((c) => (
            <CandidateRow key={c.container} c={c} onUse={() => c.url && setUrl(c.url)} />
          ))}
          {hint && (
            <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
              <div className="min-w-0 flex-1">
                <div className="text-sm font-medium">From Prowlarr’s download client “{hint.name}”</div>
                <div className="num truncate text-xs text-muted">
                  {hint.url}
                  {hint.username && <span className="text-subtle"> · user {hint.username}</span>}
                </div>
              </div>
              <Button
                size="sm"
                onClick={() => {
                  setUrl(hint.url);
                  setUsername(hint.username);
                }}
              >
                Use settings
              </Button>
            </li>
          )}
        </DetectPanel>
        {!t.configured && discover.isIdle && prowlarrHint && (
          <Notice tone="frost">
            Prowlarr already talks to Transmission at{" "}
            <span className="num">
              {prowlarrHint.host}:{prowlarrHint.port}
            </span>
            {prowlarrHint.username && <> as {prowlarrHint.username}</>}. Use <b>Detect</b> to prefill these settings — you only need to enter the password.
          </Notice>
        )}

        <FormField label="RPC URL" htmlFor={ids.url} hint="Usually http://host:9091/transmission/rpc">
          <Input
            id={ids.url}
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="http://127.0.0.1:9091/transmission/rpc"
            className="num"
            inputMode="url"
            autoComplete="off"
            spellCheck={false}
          />
        </FormField>
        <div className="grid gap-3.5 sm:grid-cols-2">
          <FormField label="Username" htmlFor={ids.user}>
            <Input id={ids.user} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" spellCheck={false} />
          </FormField>
          <FormField
            label="Password"
            htmlFor={ids.pass}
            hint={t.hasPassword ? "Saved (encrypted). Leave blank to keep it." : "Stored encrypted on this server."}
          >
            <SecretInput
              id={ids.pass}
              value={password}
              onChange={setPassword}
              placeholder={t.hasPassword ? "•••••••• (saved)" : ""}
              autoComplete="new-password"
            />
          </FormField>
        </div>

        <div className="flex flex-wrap items-center gap-2 border-t border-border pt-3.5">
          <TestOutcome result={test} />
          <div className="ml-auto flex flex-wrap gap-2">
            {t.configured && (
              <Button variant="ghost" icon={<Unplug />} onClick={() => setConfirmClear(true)}>
                Disconnect
              </Button>
            )}
            <Button onClick={() => runTest.mutate()} loading={runTest.isPending} disabled={!url}>
              Test connection
            </Button>
            <Button type="submit" variant="primary" loading={save.isPending} disabled={!url}>
              Save
            </Button>
          </div>
        </div>
      </form>
      <ConfirmDialog
        open={confirmClear}
        onOpenChange={setConfirmClear}
        title="Disconnect Transmission?"
        description="The saved URL and credentials are removed from Wargo. Transmission and its torrents are not changed."
        confirmLabel="Disconnect"
        danger
        loading={save.isPending}
        onConfirm={() => save.mutate({ url: "" }, { onSuccess: () => setConfirmClear(false) })}
      />
    </Card>
  );
}
