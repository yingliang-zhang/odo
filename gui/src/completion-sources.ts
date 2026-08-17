// Built-in @-mention sources (P3). Lazy by construction: the IPC fires only
// from provide(), which resolveCompletions calls only after the user opened
// the @ popup — listing never happens at module load.

import { listWiki, listWorkstreams } from "./api";
import type { CompletionSource } from "./completions";

export function makeWorkstreamSource(): CompletionSource {
  return {
    id: "workstream",
    async provide(ctx) {
      if (ctx.projectRoot == null) return [];
      const res = await listWorkstreams(ctx.projectRoot);
      return (res.workstreams ?? []).map((ws) => ({
        insert: `@ws:${ws.name} `,
        label: ws.name,
        detail: ws.status,
        category: "ws",
      }));
    },
  };
}

// Wiki notes are conversation-scoped on the wire (list_wiki requires the
// conversation id), so the source closes over it; ChatSurface re-registers
// on conversation switch.
export function makeWikiSource(conversationId: number): CompletionSource {
  return {
    id: "wiki",
    async provide(ctx) {
      const res = await listWiki(conversationId, ctx.projectRoot ?? undefined);
      return (res.wiki_notes ?? []).map((note) => ({
        insert: `@wiki:${note.name} `,
        label: note.name,
        detail: `epoch ${note.epoch}`,
        category: "wiki",
      }));
    },
  };
}
