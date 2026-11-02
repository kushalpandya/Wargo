// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ArrowDownToLine, Download, Eraser, WrapText } from "lucide-react";
import { cn } from "@/lib/cn";
import { downloadText } from "@/lib/download";
import { Button, IconButton } from "@/components/ui/button";
import { SearchInput } from "@/components/ui/form";
import { StatusDot } from "@/components/ui/misc";
import type { LogLine } from "./types";

const MAX_LINES = 5000;
const MAX_RENDER = 2000;

type ConnState = "connecting" | "live" | "ended" | "error";

function fmtTs(ts?: string) {
  if (!ts) return "";
  const d = new Date(ts.replace(/(\.\d{3})\d+/, "$1"));
  if (isNaN(d.getTime())) return ts.slice(0, 19);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getMonth() + 1}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function Highlight({ text, q }: { text: string; q: string }) {
  if (!q) return <>{text}</>;
  const low = text.toLowerCase();
  const out: React.ReactNode[] = [];
  let i = 0;
  let k = 0;
  for (;;) {
    const j = low.indexOf(q, i);
    if (j < 0) break;
    out.push(text.slice(i, j));
    out.push(
      <mark key={k++} className="rounded-sm bg-accent-soft text-fg ring-1 ring-accent/40">
        {text.slice(j, j + q.length)}
      </mark>,
    );
    i = j + q.length;
  }
  out.push(text.slice(i));
  return <>{out}</>;
}

/** Live-following container logs over /ws/containers/{id}/logs. */
export function LogsView({ id, name, className }: { id: string; name: string; className?: string }) {
  const [lines, setLines] = useState<LogLine[]>([]);
  const [state, setState] = useState<ConnState>("connecting");
  const [error, setError] = useState<string>("");
  const [follow, setFollow] = useState(true);
  const [wrap, setWrap] = useState(true);
  const [showTs, setShowTs] = useState(true);
  const [q, setQ] = useState("");
  const [gen, setGen] = useState(0);
  const box = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setLines([]);
    setState("connecting");
    setError("");
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(`${proto}//${location.host}/ws/containers/${encodeURIComponent(id)}/logs?tail=500`);
    let ended = false;
    ws.onopen = () => setState("live");
    ws.onmessage = (ev) => {
      try {
        const m = JSON.parse(ev.data as string) as { lines?: LogLine[]; eof?: boolean; error?: string };
        if (m.lines?.length) {
          const add = m.lines;
          setLines((prev) => {
            const next = prev.concat(add);
            return next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next;
          });
        }
        if (m.eof) {
          ended = true;
          setState("ended");
        }
        if (m.error) {
          ended = true;
          setError(m.error);
          setState("error");
        }
      } catch {
        /* ignore */
      }
    };
    ws.onerror = () => {
      if (!ended) setState("error");
    };
    ws.onclose = () => {
      if (!ended) setState((s) => (s === "error" ? s : "ended"));
    };
    return () => ws.close();
  }, [id, gen]);

  const ql = q.trim().toLowerCase();
  const visible = useMemo(() => {
    const f = ql ? lines.filter((l) => l.text.toLowerCase().includes(ql)) : lines;
    return f.length > MAX_RENDER ? f.slice(f.length - MAX_RENDER) : f;
  }, [lines, ql]);

  useLayoutEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [visible, follow]);

  const onScroll = () => {
    const el = box.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    if (atBottom !== follow) setFollow(atBottom);
  };

  const download = () => {
    const text = lines.map((l) => `${l.ts ? l.ts + " " : ""}${l.stream === "err" ? "[err] " : ""}${l.text}`).join("\n");
    downloadText(text + "\n", `${name}-${new Date().toISOString().slice(0, 19).replace(/:/g, "")}.log`);
  };

  const tone = state === "live" ? "ok" : state === "connecting" ? "warn" : state === "error" ? "crit" : "neutral";
  return (
    <div className={cn("flex min-h-0 flex-col", className)}>
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2">
        <SearchInput value={q} onChange={setQ} placeholder="Filter lines…" className="min-w-0 flex-1 basis-40" label="Filter log lines" />
        <div className="flex items-center gap-1">
          <Button size="sm" variant={follow ? "primary" : "secondary"} aria-pressed={follow} onClick={() => setFollow((f) => !f)} icon={<ArrowDownToLine />}>
            Follow
          </Button>
          <IconButton label={wrap ? "Disable line wrap" : "Wrap long lines"} size="sm" variant={wrap ? "secondary" : "ghost"} aria-pressed={wrap} onClick={() => setWrap((w) => !w)}>
            <WrapText />
          </IconButton>
          <Button size="sm" variant={showTs ? "secondary" : "ghost"} aria-pressed={showTs} onClick={() => setShowTs((v) => !v)}>
            Time
          </Button>
          <IconButton label="Clear view" size="sm" onClick={() => setLines([])}>
            <Eraser />
          </IconButton>
          <IconButton label="Download log" size="sm" onClick={download} disabled={lines.length === 0}>
            <Download />
          </IconButton>
        </div>
      </div>
      <div className="flex items-center gap-2 border-b border-border bg-surface-2 px-3 py-1 text-xs text-muted" role="status" aria-live="polite">
        <StatusDot tone={tone} pulse={state === "live"} />
        <span>
          {state === "live" ? "Live" : state === "connecting" ? "Connecting…" : state === "error" ? error || "Connection failed" : "Stream ended"}
        </span>
        <span className="num ml-auto">
          {ql ? `${visible.length} matching · ` : ""}
          {lines.length.toLocaleString()} lines
        </span>
        {(state === "ended" || state === "error") && (
          <Button size="xs" variant="ghost" onClick={() => setGen((g) => g + 1)}>
            Reconnect
          </Button>
        )}
      </div>
      <div
        ref={box}
        onScroll={onScroll}
        tabIndex={0}
        role="log"
        aria-label={`Logs for ${name}`}
        className="num min-h-0 flex-1 overflow-auto bg-bg px-3 py-2 text-[0.75rem] leading-[1.5] focus-visible:outline-none"
      >
        {visible.length === 0 ? (
          <p className="py-8 text-center text-sm text-subtle">{state === "connecting" ? "Loading…" : ql ? "No matching lines" : "No output yet"}</p>
        ) : (
          visible.map((l, i) => (
            <div
              key={i}
              className={cn("-ml-2 flex gap-3 border-l-2 pl-1.5", l.stream === "err" ? "border-warn/50" : "border-transparent", wrap ? "whitespace-pre-wrap break-all" : "whitespace-pre")}
              title={l.stream === "err" ? "stderr" : undefined}
            >
              {showTs && l.ts && <span className="shrink-0 select-none text-subtle">{fmtTs(l.ts)}</span>}
              <span className="min-w-0 text-fg">
                <Highlight text={l.text} q={ql} />
              </span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
