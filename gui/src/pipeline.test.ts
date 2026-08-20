import { describe, expect, it } from 'vitest';

import { derivePipelineStates, LANDED_FLASH_MS, pipelineHumanLocked } from './pipeline';
import type { PipelinePhase } from './pipeline';
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

  it('ignores auto_revise_product: the superseded origin stays human-decidable', () => {
    // Real journal shape (diff 21 revised into product 22): the product
    // row is newest on the origin's chain but is drainRun bookkeeping
    // (supersedeChain linkage), not activity — the origin must derive from
    // its governing round row (revise = escape hatch), never in_flight.
    const events = [
      autoRow(10, { action: 'auto_land_started', diff_id: 21, stage: 'verify' }),
      autoRow(11, { action: 'auto_land_started', diff_id: 21, stage: 'panel' }),
      autoRow(12, { action: 'auto_revise_round', diff_id: 21, origin_diff_id: 21, round: 1 }),
      autoRow(13, { action: 'auto_revise_product', origin_diff_id: 21, product_diff_id: 22 }),
      autoRow(14, { action: 'auto_land_started', diff_id: 22, stage: 'verify' }),
      autoRow(15, { action: 'auto_land_blocked', diff_id: 22, reason: 'verify_failed' }),
    ];
    const states = derivePipelineStates(events, [21, 22], true);
    expect(states).toEqual([
      { diffId: 21, phase: 'revise', round: 1, lastSeq: 12 },
      { diffId: 22, phase: 'blocked', reason: 'verify_failed', lastSeq: 15 },
    ]);
    expect(states.every((s) => !pipelineHumanLocked(s))).toBe(true);
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

  it('names the silent stage from started breadcrumbs, newest wins', () => {
    const verify = [autoRow(6, { action: 'auto_land_started', diff_id: 8, stage: 'verify' })];
    expect(derivePipelineStates(verify, [8], true)).toEqual([
      { diffId: 8, phase: 'in_flight', stage: 'verify', lastSeq: 6 },
    ]);
    // verify → panel: the panel breadcrumb supersedes (latest row wins).
    const panel = [
      ...verify,
      autoRow(7, { action: 'auto_land_started', diff_id: 8, stage: 'panel' }),
    ];
    expect(derivePipelineStates(panel, [8], true)).toEqual([
      { diffId: 8, phase: 'in_flight', stage: 'panel', lastSeq: 7 },
    ]);
  });

  it('degrades unknown started stages to plain in_flight', () => {
    // Forward-compat (lock rule 4): a daemon newer than this GUI journals
    // a stage the chip has no label for — plain in flight, never a crash.
    const events = [autoRow(6, { action: 'auto_land_started', diff_id: 8, stage: 'future_stage' })];
    expect(derivePipelineStates(events, [8], true)).toEqual([
      { diffId: 8, phase: 'in_flight', lastSeq: 6 },
    ]);
  });

  it('the verdict supersedes the started breadcrumb', () => {
    const events = [
      autoRow(6, { action: 'auto_land_started', diff_id: 8, stage: 'panel' }),
      autoRow(9, { action: 'moa_review', diff_id: 8, consensus_verdict: 'accept' }),
    ];
    expect(derivePipelineStates(events, [8], true)[0].phase).toBe('landing');
  });
});

describe('pipelineHumanLocked', () => {
  // The review surfaces (DiffViewer card, ReviewInbox row) read this ONE
  // predicate to decide whether the human-action buttons lock while the
  // daemon is mid-pipeline — pin the full phase truth table so the two
  // surfaces can never drift, and so a phase re-map here is a deliberate,
  // reviewed change (e.g. revising revise/blocked would strip the human's
  // escape hatches).
  it('locks only the actively-working phases', () => {
    const cases: Array<[PipelinePhase, boolean]> = [
      ['queued', false], // pre-start gap — nothing is racing yet
      ['in_flight', true], // verify/panel/refresh — verdict in flight
      ['landing', true], // moa accept → land window
      ['landed', false], // transient flash; diff is leaving the pending list
      ['blocked', false], // hard stop: hands the decision TO the human
      ['suspended', false], // human accept is the ladder's only resume
      ['revise', false], // mid-ladder escape hatch stays open
      ['hidden', false],
    ];
    for (const [phase, want] of cases) {
      expect(pipelineHumanLocked({ diffId: 1, phase, lastSeq: 0 })).toBe(want);
    }
  });

  it('no derivation (foreign-conversation inbox row) means unlocked', () => {
    expect(pipelineHumanLocked(undefined)).toBe(false);
  });
});
