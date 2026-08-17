import { describe, expect, it } from 'vitest';

import { derivePipelineStates, LANDED_FLASH_MS } from './pipeline';
import type { EventPayload, OdoEvent } from './types';

function ev(seq: number, type: OdoEvent['type'], payload: EventPayload = {}, created_at = '2026-08-17T00:00:00.000Z'): OdoEvent {
  return { id: seq, conversation_id: 1, seq, type, payload, created_at };
}

const autoRow = (seq: number, payload: EventPayload, created_at?: string) =>
  ev(seq, 'review_action', { actor: 'auto_panel', ...payload }, created_at);

describe('derivePipelineStates', () => {
  it('returns nothing when the pref is off', () => {
    expect(derivePipelineStates([], [1], false)).toEqual([]);
  });

  it('marks pending diffs with no auto row as queued', () => {
    const states = derivePipelineStates([], [3, 1], true);
    expect(states.map((s) => [s.diffId, s.phase])).toEqual([
      [1, 'queued'],
      [3, 'queued'],
    ]);
    expect(states[0].lastSeq).toBe(0);
  });

  it('reports blocked with the journaled reason, while pending', () => {
    const events = [autoRow(10, { action: 'auto_land_blocked', diff_id: 2, reason: 'verify failed' })];
    const states = derivePipelineStates(events, [2], true);
    expect(states).toEqual([{ diffId: 2, phase: 'blocked', reason: 'verify failed', lastSeq: 10 }]);
  });

  it('reports revise rounds via origin chain to a pending root', () => {
    const events = [
      autoRow(9, { action: 'auto_revise_round', diff_id: 2, origin_diff_id: 1, round: 2 }),
    ];
    const states = derivePipelineStates(events, [1], true);
    expect(states).toEqual([{ diffId: 1, phase: 'revise', round: 2, lastSeq: 9 }]);
  });

  it('reads moa_review consensus accept as landing, otherwise in_flight', () => {
    const accept = [autoRow(5, { action: 'moa_review', diff_id: 1, consensus_verdict: 'accept' })];
    const split = [autoRow(5, { action: 'moa_review', diff_id: 1, consensus_verdict: 'revise' })];
    expect(derivePipelineStates(accept, [1], true)[0].phase).toBe('landing');
    expect(derivePipelineStates(split, [1], true)[0].phase).toBe('in_flight');
  });

  it('flashes landed only inside the window and by newest accept', () => {
    const now = Date.parse('2026-08-17T00:00:03.000Z');
    const fresh = autoRow(7, { action: 'accept', diff_id: 4 }, '2026-08-17T00:00:01.000Z');
    const states = derivePipelineStates([fresh], [4], true, now);
    expect(states[0].phase).toBe('landed');
    expect(states[0].landedUntil).toBe(Date.parse('2026-08-17T00:00:01.000Z') + LANDED_FLASH_MS);
    // Past the window: dropped entirely, even though the accept row exists.
    const laterNow = Date.parse('2026-08-17T00:00:10.000Z');
    expect(derivePipelineStates([fresh], [4], true, laterNow)).toEqual([]);
  });

  it('keeps the landed flash after the diff leaves the pending list', () => {
    const now = Date.parse('2026-08-17T00:00:03.000Z');
    const fresh = autoRow(7, { action: 'accept', diff_id: 4 }, '2026-08-17T00:00:01.000Z');
    const states = derivePipelineStates([fresh], [], true, now);
    expect(states).toHaveLength(1);
    expect(states[0].phase).toBe('landed');
  });

  it('suspends every pending diff while the ladder is suspended', () => {
    const events = [
      autoRow(10, { action: 'auto_land_blocked', diff_id: 2, reason: 'verify failed' }),
      ev(11, 'memory_update', { layer: 'auto_land', cause: 'ladder_suspended' }),
    ];
    const states = derivePipelineStates(events, [2, 3], true);
    expect(states.map((s) => s.phase)).toEqual(['suspended', 'suspended']);
  });

  it('treats unknown non-terminal actions as in_flight', () => {
    const events = [autoRow(6, { action: 'refresh_attempted', diff_id: 8, outcome: 'clean' })];
    const states = derivePipelineStates(events, [8], true);
    expect(states).toEqual([{ diffId: 8, phase: 'in_flight', refreshed: true, lastSeq: 6 }]);
  });
});
