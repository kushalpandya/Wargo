// SPDX-License-Identifier: AGPL-3.0-only
// Helpers for the fake API: every handler answers like Wargo's Go server
// (JSON bodies, {code, message} errors) after a short, realistic delay.
import { delay, http, HttpResponse, type HttpResponseResolver, type PathParams } from "msw";

type Params = PathParams;
type Resolver = HttpResponseResolver<Params, any>; // eslint-disable-line @typescript-eslint/no-explicit-any

/** Network-ish latency so spinners and skeletons are visible briefly. */
async function latency() {
  await delay(40 + Math.random() * 120);
}

function wrap(fn: Resolver): Resolver {
  return async (info) => {
    await latency();
    return fn(info);
  };
}

export const api = {
  get: (path: string, fn: Resolver) => http.get(path, wrap(fn)),
  post: (path: string, fn: Resolver) => http.post(path, wrap(fn)),
  put: (path: string, fn: Resolver) => http.put(path, wrap(fn)),
  patch: (path: string, fn: Resolver) => http.patch(path, wrap(fn)),
  del: (path: string, fn: Resolver) => http.delete(path, wrap(fn)),
};

export const json = <T>(data: T, status = 200) => HttpResponse.json(data as never, { status });
export const noContent = () => new HttpResponse(null, { status: 204 });

/** An error in Wargo's format. */
export const fail = (status: number, code: string, message: string, data?: unknown) =>
  HttpResponse.json({ code, message, ...(data !== undefined ? { data } : {}) }, { status });

export const badRequest = (message: string) => fail(400, "bad_request", message);
export const notFound = (message = "Not found") => fail(404, "not_found", message);

/** For actions the demo can't meaningfully fake. */
export const notInDemo = (what: string) => fail(501, "not_in_demo", `${what} isn't available in the demo.`);

/** Parses a JSON request body (an empty body gives {}). */
export async function body<T>(request: Request): Promise<T> {
  const text = await request.text();
  return (text ? JSON.parse(text) : {}) as T;
}

/** Query parameters of a request. */
export const query = (request: Request) => new URL(request.url).searchParams;
