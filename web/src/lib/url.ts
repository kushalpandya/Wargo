// SPDX-License-Identifier: AGPL-3.0-only
// Guards for URLs that come from the server or from third parties
// (launcher tiles, indexer pages, container icons). React already blocks
// `javascript:` in href, but `data:`, `blob:`, `file:` and custom schemes
// still get through, so links to untrusted targets go through these.

/** True for absolute http(s) URLs with a host. */
export function isHttpUrl(v: string | null | undefined): v is string {
  if (!v) return false;
  try {
    const u = new URL(v);
    return (u.protocol === "http:" || u.protocol === "https:") && !!u.host;
  } catch {
    return false;
  }
}

/** Returns v when it is an absolute http(s) URL, else undefined. */
export function safeHttpUrl(v: string | null | undefined): string | undefined {
  return isHttpUrl(v) ? v : undefined;
}

/** Returns v when it is a same-origin absolute path ("/api/…"), else undefined. */
export function safeLocalPath(v: string | null | undefined): string | undefined {
  // "//host" and "/\host" are protocol-relative in browsers.
  return v && v.startsWith("/") && !v.startsWith("//") && !v.startsWith("/\\") ? v : undefined;
}

/** Returns v when it is a data: URL for a raster image (e.g. a QR code). */
export function safeImageDataUrl(v: string | null | undefined): string | undefined {
  return v && /^data:image\/(png|gif|jpeg|webp);base64,[a-z0-9+/=]+$/i.test(v) ? v : undefined;
}
