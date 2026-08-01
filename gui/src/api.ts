// Thin type-safe wrappers over the Tauri commands. The Rust layer forwards
// each call to the Go daemon's Unix socket; these functions never contain
// business logic — the daemon is the single source of truth (Invariant 1).

import { invoke } from "@tauri-apps/api/core";
import type {
  AcceptDiffResponse,
  BootstrapResponse,
  PollEventsResponse,
  RejectDiffResponse,
  SendMessageResponse,
} from "./types";

// Tauri v2 maps JS camelCase args onto the Rust snake_case parameters.

export function bootstrap(projectRoot?: string): Promise<BootstrapResponse> {
  return invoke<BootstrapResponse>("bootstrap", { projectRoot: projectRoot ?? null });
}

export function sendMessage(conversationId: number, text: string): Promise<SendMessageResponse> {
  return invoke<SendMessageResponse>("send_message", { conversationId, text });
}

export function pollEvents(conversationId: number, afterSeq: number): Promise<PollEventsResponse> {
  return invoke<PollEventsResponse>("poll_events", { conversationId, afterSeq });
}

export function acceptDiff(diffId: number): Promise<AcceptDiffResponse> {
  return invoke<AcceptDiffResponse>("accept_diff", { diffId });
}

export function rejectDiff(diffId: number): Promise<RejectDiffResponse> {
  return invoke<RejectDiffResponse>("reject_diff", { diffId });
}

// Daemon-level failures arrive with ok:false; transport failures (invoke
// rejects) arrive as thrown strings. Normalize both into Error.
export function unwrap<T extends { ok: boolean; error?: string }>(resp: T): T {
  if (!resp.ok) {
    throw new Error(resp.error ?? "daemon returned an unknown error");
  }
  return resp;
}

export function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message;
  if (typeof e === "string") return e;
  return String(e);
}
