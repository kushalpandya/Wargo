// SPDX-License-Identifier: AGPL-3.0-only

/**
 * Sorts an HTTP answer into Wargo's own ("ok" or "error") and answers that
 * came from something in between, such as a Cloudflare error page or a
 * proxy's login page ("unreachable"). Wargo's API always answers in JSON, so
 * a JSON 401 still means signed out and a JSON 503 is Wargo's own error.
 */
export function classifyResponse(r: { path: string; status: number; contentType: string }): "ok" | "unreachable" | "error" {
  const ct = r.contentType.toLowerCase();
  if (!ct.includes("json")) {
    if (r.path.startsWith("/api/") && ct.includes("text/html")) return "unreachable";
    if (r.status === 0 || r.status >= 500) return "unreachable";
  }
  return r.status >= 200 && r.status < 300 ? "ok" : "error";
}

/**
 * Query retry rule: up to two retries for network failures, timeouts and
 * server errors, none for Wargo's own 4xx answers (they won't change).
 */
export function shouldRetry(failures: number, err: { status: number; code: string } | null): boolean {
  if (failures >= 2) return false;
  if (!err || err.code === "unreachable") return true;
  return err.status >= 500;
}

const errName = (x: unknown) => (typeof x === "object" && x !== null && "name" in x ? String((x as { name: unknown }).name) : "");

/**
 * Sorts a rejected fetch: a cancellation by the app stays a cancellation;
 * a timeout (even one a browser reports as AbortError, judged by the
 * signal's reason) or a network failure means the server can't be reached.
 */
export function fetchFailure(e: unknown, signalReason: unknown): "abort" | "unreachable" {
  if (errName(e) === "TimeoutError" || errName(signalReason) === "TimeoutError") return "unreachable";
  return errName(e) === "AbortError" ? "abort" : "unreachable";
}
