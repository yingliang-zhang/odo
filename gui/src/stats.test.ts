import { describe, expect, it } from 'vitest';

import {
  contextWindowTokens,
  deriveLastPrompt,
  deriveTurnStats,
  FALLBACK_CONTEXT_WINDOW,
  formatBytes,
  formatTokens,
  parseReviewModels,
} from './stats';
import type { EventPayload, OdoEvent } from './types';

function ev(seq: number, type: OdoEvent['type'], payload: EventPayload = {}, created_at = '2026-08-17T00:00:00.000Z'): OdoEvent {
  return { id: seq, conversation_id: 1, seq, type, payload, created_at };
}

describe('contextWindowTokens', () => {
  it('strips provider prefixes, lowercases, and falls back', () => {
    expect(contextWindowTokens(null)).toBe(FALLBACK_CONTEXT_WINDOW);
    expect(contextWindowTokens('t9s/kimi-k3')).toBe(350_000);
    expect(contextWindowTokens('KIMI-K3')).toBe(350_000);
    expect(contextWindowTokens('unknown-model')).toBe(FALLBACK_CONTEXT_WINDOW);
  });
});

describe('formatBytes / formatTokens', () => {
  it('formats byte and token magnitudes', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(2048)).toBe('2.0 KB');
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MB');
    expect(formatTokens(500)).toBe('500 tok');
    expect(formatTokens(42_000)).toBe('42.0k tok');
  });
});

describe('deriveLastPrompt', () => {
  it('returns the newest closure with bytes and layers', () => {
    const events = [
      ev(3, 'user_message', { total_prompt_bytes: 1000, receipt: { 'wiki/index.md': 'a' } }),
      ev(8, 'review_action', {
        action: 'run_prompt',
        total_prompt_bytes: 4096,
        prompt_sha16: 'deadbeef',
        receipt: { 'odo#memory-map': 'b', 'journal#todo': 'c' },
      }),
      ev(9, 'agent_text', { text: 'no closure here' }),
    ];
    const snap = deriveLastPrompt(events);
    expect(snap?.bytes).toBe(4096);
    expect(snap?.seq).toBe(8);
    expect(snap?.sha16).toBe('deadbeef');
    expect(snap?.layers).toEqual(['odo#memory-map', 'journal#todo']);
  });

  it('returns null without a closure', () => {
    expect(deriveLastPrompt([ev(1, 'user_message', { text: 'hi' })])).toBeNull();
  });
});

describe('deriveTurnStats', () => {
  const start = ev(10, 'user_message', { total_prompt_bytes: 100 }, '2026-08-17T00:00:00.000Z');
  const done = ev(15, 'agent_done', {}, '2026-08-17T00:00:02.500Z');

  it('is null without a done event', () => {
    expect(deriveTurnStats(start, [], undefined)).toBeNull();
  });

  it('derives wall time, sizes, and tool calls from the run events', () => {
    const events = [
      ev(11, 'agent_text', { text: 'hello' }), // 5 bytes
      ev(12, 'agent_tool_call', { tool: 'read' }),
      ev(13, 'agent_text', { text: 'héllo' }), // 6 bytes (é = 2 UTF-8 bytes)
      ev(14, 'agent_tool_call', { tool: 'grep' }),
    ];
    const stats = deriveTurnStats(start, events, done);
    expect(stats?.wallMs).toBe(2500);
    expect(stats?.inputBytes).toBe(100);
    expect(stats?.outputBytes).toBe(11);
    expect(stats?.toolsCalls).toBe(2);
  });

  it('reports null input bytes when the start carries none', () => {
    const noBytes = ev(10, 'user_message', {}, '2026-08-17T00:00:00.000Z');
    expect(deriveTurnStats(noBytes, [], done)?.inputBytes).toBeNull();
  });
});

describe('parseReviewModels', () => {
  it('parses model@provider at the last @ and drops malformed entries', () => {
    expect(parseReviewModels('kimi-k3@t9s, glm-5.2@zai')).toEqual([
      { model: 'kimi-k3', provider: 't9s' },
      { model: 'glm-5.2', provider: 'zai' },
    ]);
    expect(parseReviewModels('we@ird@prov')).toEqual([{ model: 'we@ird', provider: 'prov' }]);
    expect(parseReviewModels('noat, @only-provider, model@, ,')).toEqual([]);
  });
});
