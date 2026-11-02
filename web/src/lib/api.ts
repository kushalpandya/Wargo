// SPDX-License-Identifier: AGPL-3.0-only

/** Error returned by the Wargo API ({code, message, data}). */
export class ApiError extends Error {
  status: number;
  code: string;
  data?: unknown;
  constructor(status: number, code: string, message: string, data?: unknown) {
    super(message);
    this.status = status;
    this.code = code;
    this.data = data;
  }
}

export type ElevationCredential =
  | { method: "sudo"; password: string }
  | { method: "passkey"; challenge: string; assertion: unknown };

export interface ElevationRequest {
  action: string;
  target: string;
  methods: { sudo: boolean; passkey: boolean; noNewPrivsHint?: boolean };
}

type ElevationPrompt = (req: ElevationRequest) => Promise<ElevationCredential | null>;

let elevationPrompt: ElevationPrompt | null = null;
let onUnauthorized: (() => void) | null = null;

/** Registered by <ElevationProvider/>. */
export function setElevationPrompt(fn: ElevationPrompt | null) {
  elevationPrompt = fn;
}

/** Called on 401 so the app can route to the login screen. */
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn;
}

function csrf(): string {
  const m = document.cookie.match(/(?:^|;\s*)wargo_csrf=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}

function encodeElevation(c: ElevationCredential): string {
  const bytes = new TextEncoder().encode(JSON.stringify(c));
  let bin = "";
  bytes.forEach((b) => (bin += String.fromCharCode(b)));
  return btoa(bin);
}

export interface RequestOptions {
  method?: string;
  body?: unknown;
  /** Raw body (e.g. Blob for uploads); skips JSON encoding. */
  raw?: BodyInit;
  headers?: Record<string, string>;
  signal?: AbortSignal;
  /** Return the Response instead of parsing JSON. */
  response?: boolean;
}

async function send(path: string, opts: RequestOptions, elevation?: ElevationCredential): Promise<Response> {
  const method = opts.method ?? (opts.body !== undefined || opts.raw !== undefined ? "POST" : "GET");
  const headers: Record<string, string> = { Accept: "application/json", ...opts.headers };
  if (method !== "GET" && method !== "HEAD") headers["X-Wargo-CSRF"] = csrf();
  let body: BodyInit | undefined = opts.raw;
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }
  if (elevation) headers["X-Wargo-Elevation"] = encodeElevation(elevation);
  return fetch(path, { method, headers, body, signal: opts.signal, credentials: "same-origin" });
}

async function toError(res: Response): Promise<ApiError> {
  try {
    const j = await res.json();
    return new ApiError(res.status, j.code ?? "error", j.message ?? res.statusText, j.data);
  } catch {
    return new ApiError(res.status, "error", res.statusText || `HTTP ${res.status}`);
  }
}

/**
 * Calls the API. If the server answers `elevation_required`, the user is
 * prompted (sudo password or passkey) and the request is retried once with
 * the credential attached. Credentials are never stored.
 */
export async function request<T = unknown>(path: string, opts: RequestOptions = {}): Promise<T> {
  let res = await send(path, opts);
  if (res.status === 403 || res.status === 401) {
    const err = await toError(res.clone());
    if (err.code === "elevation_required" && elevationPrompt) {
      let attempt = 0;
      for (;;) {
        const cred = await elevationPrompt(err.data as ElevationRequest);
        if (!cred) throw new ApiError(403, "elevation_canceled", "Canceled");
        res = await send(path, opts, cred);
        if (cred.method === "sudo") (cred as { password: string }).password = "";
        if (res.status === 401 && attempt < 2) {
          const e2 = await toError(res.clone());
          if (e2.code === "elevation_failed") {
            attempt++;
            (err.data as ElevationRequest & { error?: string }).error = e2.message;
            continue;
          }
        }
        break;
      }
    } else if (res.status === 401 && onUnauthorized && !path.startsWith("/api/auth/")) {
      onUnauthorized();
    }
  }
  if (!res.ok) throw await toError(res);
  if (opts.response) return res as unknown as T;
  if (res.status === 204) return undefined as T;
  const ct = res.headers.get("Content-Type") ?? "";
  return (ct.includes("json") ? res.json() : res.text()) as Promise<T>;
}

export const api = {
  get: <T>(path: string, signal?: AbortSignal) => request<T>(path, { signal }),
  post: <T>(path: string, body?: unknown) => request<T>(path, { method: "POST", body: body ?? {} }),
  put: <T>(path: string, body?: unknown) => request<T>(path, { method: "PUT", body: body ?? {} }),
  patch: <T>(path: string, body?: unknown) => request<T>(path, { method: "PATCH", body: body ?? {} }),
  del: <T>(path: string, body?: unknown) => request<T>(path, { method: "DELETE", body }),
  request,
};

/** Builds a query string, skipping empty values. */
export function qs(params: Record<string, string | number | boolean | undefined | null>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}
