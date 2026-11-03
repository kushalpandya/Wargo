// SPDX-License-Identifier: AGPL-3.0-only
// Every fake endpoint, in matching order: the sign-in guard first, the
// features, then a catch-all so nothing silently hits the network.
import { http } from "msw";
import { notInDemo } from "./core/http";
import { eventsHandler } from "./core/bus";
import { authHandlers, guard } from "./features/auth";
import { systemHandlers } from "./features/system";
import { extraHandlers } from "./features";

export const handlers = [
  guard,
  ...authHandlers,
  ...systemHandlers,
  ...extraHandlers,
  eventsHandler,
  http.all("/api/*", ({ request }) => {
    const u = new URL(request.url);
    console.info("[demo] not faked:", request.method, u.pathname);
    return notInDemo("This action");
  }),
];
