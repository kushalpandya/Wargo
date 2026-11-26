// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { Link, useParams } from "react-router";
import { BotMessageSquare, Check, SearchX, X } from "lucide-react";
import { ApiError } from "@/lib/api";
import { ago, datetime } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Badge, EmptyState, ErrorNote, Notice, OutputBlock, PageHeader, Skeleton, type Tone } from "@/components/ui/misc";
import { useApproval, useDecide, type Approval, type ApprovalState } from "./api";
import { approvalErrorMessage, prettyJSON, timeLeft } from "./approval";

const stateBadge: Record<ApprovalState, { tone: Tone; label: string }> = {
  pending: { tone: "warn", label: "Waiting" },
  approved: { tone: "ok", label: "Approved" },
  denied: { tone: "neutral", label: "Denied" },
  expired: { tone: "neutral", label: "Expired" },
};

function Part({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <div>
        <h3 className="text-sm font-medium">{title}</h3>
        {hint && <p className="text-xs text-muted">{hint}</p>}
      </div>
      {children}
    </div>
  );
}

/** What came of an approved request: the handler's answer, or the job it started. */
function Outcome({ p }: { p: Approval }) {
  const failed = (p.status ?? 0) >= 400;
  const result = prettyJSON(p.result);
  return (
    <div className="flex flex-col gap-3">
      <Notice tone={failed ? "warn" : "ok"} icon={<Check />}>
        {failed ? "Approved, but the action failed." : "Approved. It ran as the agent asked."}
        {p.jobId && (
          <>
            {" "}
            It started a job; follow it on the{" "}
            <Link to="/updates" className="font-medium underline underline-offset-2">
              Updates page
            </Link>
            .
          </>
        )}
      </Notice>
      {result && (
        <Part title="Result">
          <OutputBlock label="Result">{result}</OutputBlock>
        </Part>
      )}
    </div>
  );
}

function decideError(e: unknown): string | null {
  if (e instanceof ApiError) return approvalErrorMessage(e);
  return e instanceof Error ? e.message : String(e);
}

/** The approval page an agent's admin request links to: what will run, then Approve or Deny. */
export default function Approve() {
  const id = useParams().id ?? "";
  const q = useApproval(id);
  const decide = useDecide(id);
  const p = q.data;
  const now = useNow(1000, p?.state === "pending");
  const left = p ? timeLeft(p.expires, now) : null;
  // The countdown can reach zero before the server's sweep marks it
  // expired; a request already running does not expire.
  const state: ApprovalState | undefined = p && (p.state === "pending" && !left && !p.running ? "expired" : p.state);
  const err = decide.error ? decideError(decide.error) : null;
  const run = (d: "approve" | "deny") => decide.mutate(d, { onError: () => q.refetch() });

  let body: ReactNode;
  if (q.isLoading) {
    body = <Skeleton className="h-80" />;
  } else if (!p) {
    body =
      q.error instanceof ApiError && q.error.status === 404 ? (
        <Card>
          <EmptyState icon={<SearchX />} title="Request not found">
            {approvalErrorMessage(q.error)}
          </EmptyState>
        </Card>
      ) : (
        <ErrorNote error={q.error} />
      );
  } else {
    const running = state === "pending" && !!p.running;
    const badge = running ? { tone: "accent" as Tone, label: "Running" } : stateBadge[state ?? "pending"];
    const reqBody = prettyJSON(p.body);
    const args = prettyJSON(p.args);
    body = (
      <Card>
        <CardHeader title={p.agent.name} icon={<BotMessageSquare />} tint="sky" meta={p.tool} actions={<Badge tone={badge.tone}>{badge.label}</Badge>} />
        <CardBody className="flex flex-col gap-5">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
            <dt className="text-muted">Agent</dt>
            <dd className="min-w-0 [overflow-wrap:anywhere]">{p.agent.name}</dd>
            <dt className="text-muted">Tool</dt>
            <dd className="num min-w-0 [overflow-wrap:anywhere]">{p.tool}</dd>
            <dt className="text-muted">Asked</dt>
            <dd title={datetime(p.created)}>{ago(p.created)}</dd>
            {state === "pending" && (
              <>
                <dt className="text-muted">Time left</dt>
                <dd className="num" aria-live="off">
                  {left}
                </dd>
              </>
            )}
          </dl>
          <Part title="Summary" hint="Written when the agent asked; it may include the agent's own words. The request below is what runs.">
            <p className="text-sm [overflow-wrap:anywhere]">{p.summary}</p>
          </Part>
          <Part
            title={state === "pending" ? "Exactly what will run" : "The request"}
            hint={state === "pending" ? "This request runs unchanged, as the agent, if you approve." : undefined}
          >
            <OutputBlock label="Request">{`${p.method} ${p.path}${reqBody ? `\n\n${reqBody}` : ""}`}</OutputBlock>
          </Part>
          {args && (
            <details className="rounded-md border border-border">
              <summary className="cursor-pointer select-none rounded-md px-3 py-2 text-sm font-medium hover:bg-surface-2">The agent's arguments</summary>
              <div className="border-t border-border p-3">
                <OutputBlock label="Arguments">{args}</OutputBlock>
              </div>
            </details>
          )}
          {err && <ErrorNote error={err} />}
          {state === "approved" && <Outcome p={p} />}
          {state === "denied" && <Notice tone="neutral" icon={<X />}>Denied. Nothing ran.</Notice>}
          {state === "expired" && <Notice tone="neutral">This request expired before it was decided, so nothing ran. The agent can ask again.</Notice>}
          {running && <Notice>Approved and running now. This page updates when it finishes.</Notice>}
          {state === "pending" && (
            <div className="flex flex-wrap justify-end gap-2">
              <Button
                variant="outline-danger"
                icon={<X />}
                loading={decide.isPending && decide.variables === "deny"}
                disabled={decide.isPending || running}
                onClick={() => run("deny")}
              >
                Deny
              </Button>
              <Button
                variant="primary"
                icon={<Check />}
                loading={decide.isPending && decide.variables === "approve"}
                disabled={decide.isPending || running}
                onClick={() => run("approve")}
              >
                Approve
              </Button>
            </div>
          )}
        </CardBody>
      </Card>
    );
  }

  return (
    <div className="mx-auto w-full max-w-2xl min-w-0">
      <PageHeader title="Agent request" parent={{ label: "Settings", to: "/settings/general" }} />
      {body}
    </div>
  );
}
