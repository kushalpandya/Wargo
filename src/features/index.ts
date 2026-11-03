// SPDX-License-Identifier: AGPL-3.0-only
// Feature modules beyond sign-in and system. Each exports its handlers
// (HTTP and WebSocket) and registers its live topics on import.
import type { RequestHandler, WebSocketHandler } from "msw";
import { jobHandlers } from "../core/jobs";
import { elevationHandlers } from "../core/elevation";
import { handlers as containers } from "./containers";
import { handlers as processes } from "./processes";
import { handlers as terminal } from "./terminal";
import { handlers as files } from "./files";
import { handlers as updates } from "./updates";
import { handlers as torrents } from "./torrents";
import { handlers as integrations } from "./integrations";
import { handlers as launcher } from "./launcher";
import { handlers as settings } from "./settings";

export const extraHandlers: (RequestHandler | WebSocketHandler)[] = [
  ...jobHandlers,
  ...elevationHandlers,
  ...containers,
  ...processes,
  ...terminal,
  ...files,
  ...updates,
  ...torrents,
  ...integrations,
  ...launcher,
  ...settings,
];
