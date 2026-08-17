// Completion source interface — inspired by Hermes's ComposerAtCompletionSource.
// Each source provides items for the @-mention popup. Sources are isolated:
// a throwing source doesn't kill the popup.
export interface CompletionItem {
  /** Text to insert into the textarea (replaces the @query). */
  insert: string;
  /** Display label in the popup. */
  label: string;
  /** Optional secondary text (e.g. file path, workstream status). */
  detail?: string;
  /** Optional icon or category prefix. */
  category?: string;
}

export interface CompletionContext {
  /** The query string after the @ (may be empty). */
  query: string;
  /** The project root for resolving paths. */
  projectRoot: string | null;
}

export interface CompletionSource {
  /** Unique identifier for this source. */
  id: string;
  /** Returns completion items for the given context. */
  provide(ctx: CompletionContext): CompletionItem[] | Promise<CompletionItem[]>;
}

// In-code registry — no plugin SDK, just a typed array.
const sources: CompletionSource[] = [];

export function registerCompletionSource(source: CompletionSource) {
  sources.push(source);
  return () => { const i = sources.indexOf(source); if (i >= 0) sources.splice(i, 1); };
}

export async function resolveCompletions(ctx: CompletionContext): Promise<CompletionItem[]> {
  const results: CompletionItem[] = [];
  for (const source of sources) {
    try {
      const items = await source.provide(ctx);
      results.push(...items);
    } catch (e) {
      console.warn(`completion source "${source.id}" failed:`, e);
    }
  }
  return results;
}

// Word-start guard: only triggers when `@` opens a word (line start or preceded by
// whitespace) and the query carries no whitespace — never inside emails or code.
// The query ceases to be live as soon as the caret leaves the `word@query` span,
// because the regex only examines text before the caret.
export function detectAtQuery(text: string, caret: number): string | null {
  const before = text.slice(0, caret);
  const m = /(?:^|\s)@([^\s@]*)$/.exec(before);
  return m == null ? null : m[1];
}
