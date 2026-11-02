// SPDX-License-Identifier: AGPL-3.0-only
// Thin helpers around WebAuthn JSON <-> binary conversion.

function b64urlToBuf(s: string): ArrayBuffer {
  const pad = "=".repeat((4 - (s.length % 4)) % 4);
  const bin = atob((s + pad).replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

function bufToB64url(b: ArrayBuffer): string {
  const bytes = new Uint8Array(b);
  let bin = "";
  bytes.forEach((x) => (bin += String.fromCharCode(x)));
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function webauthnSupported(): boolean {
  return typeof window.PublicKeyCredential !== "undefined" && window.isSecureContext;
}

type Json = Record<string, unknown>;

/** Server sends {publicKey: {...}} (go-webauthn); unwrap it. */
function unwrap(o: Json): Json {
  return (o.publicKey as Json) ?? o;
}

export async function createCredential(options: Json): Promise<Json> {
  const pk = unwrap(options);
  const PKC = PublicKeyCredential as unknown as { parseCreationOptionsFromJSON?: (j: unknown) => PublicKeyCredentialCreationOptions };
  const opts: PublicKeyCredentialCreationOptions = PKC.parseCreationOptionsFromJSON
    ? PKC.parseCreationOptionsFromJSON(pk)
    : ({
        ...(pk as object),
        challenge: b64urlToBuf(pk.challenge as string),
        user: { ...(pk.user as Json), id: b64urlToBuf((pk.user as Json).id as string) },
        excludeCredentials: ((pk.excludeCredentials as Json[]) ?? []).map((c) => ({ ...c, id: b64urlToBuf(c.id as string) })),
      } as unknown as PublicKeyCredentialCreationOptions);
  const cred = (await navigator.credentials.create({ publicKey: opts })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No credential created");
  return toJSON(cred);
}

export async function getAssertion(options: Json): Promise<Json> {
  const pk = unwrap(options);
  const PKC = PublicKeyCredential as unknown as { parseRequestOptionsFromJSON?: (j: unknown) => PublicKeyCredentialRequestOptions };
  const opts: PublicKeyCredentialRequestOptions = PKC.parseRequestOptionsFromJSON
    ? PKC.parseRequestOptionsFromJSON(pk)
    : ({
        ...(pk as object),
        challenge: b64urlToBuf(pk.challenge as string),
        allowCredentials: ((pk.allowCredentials as Json[]) ?? []).map((c) => ({ ...c, id: b64urlToBuf(c.id as string) })),
      } as unknown as PublicKeyCredentialRequestOptions);
  const cred = (await navigator.credentials.get({ publicKey: opts })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No passkey selected");
  return toJSON(cred);
}

function toJSON(cred: PublicKeyCredential): Json {
  const c = cred as PublicKeyCredential & { toJSON?: () => Json };
  if (typeof c.toJSON === "function") return c.toJSON() as unknown as Json;
  const r = cred.response as AuthenticatorAttestationResponse & AuthenticatorAssertionResponse;
  const response: Json = { clientDataJSON: bufToB64url(r.clientDataJSON) };
  if ("attestationObject" in r && r.attestationObject) {
    response.attestationObject = bufToB64url(r.attestationObject);
    response.transports = r.getTransports?.() ?? [];
  }
  if ("authenticatorData" in r && r.authenticatorData) {
    response.authenticatorData = bufToB64url(r.authenticatorData);
    response.signature = bufToB64url(r.signature);
    if (r.userHandle) response.userHandle = bufToB64url(r.userHandle);
  }
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response,
    clientExtensionResults: cred.getClientExtensionResults(),
    authenticatorAttachment: cred.authenticatorAttachment,
  };
}
