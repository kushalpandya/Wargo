// SPDX-License-Identifier: AGPL-3.0-only
import type { RequestHandler, WebSocketHandler } from "msw";

export const handlers: (RequestHandler | WebSocketHandler)[] = [];
