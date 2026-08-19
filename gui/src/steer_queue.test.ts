import { describe, expect, it } from 'vitest';

import { deriveActivePrompt, deriveSteerQueue, latestRunSteerSeqs } from './steer_queue';
import type { EventPayload, OdoEvent } from './types';

function ev(seq: number, type: OdoEvent['type'], payload: EventPayload = {}): OdoEvent {
  return { id: seq, conversation_id: 1, seq, type, payload, created_at: '2026-08-17T00:00:00.000Z' };
}

describe('deriveSteerQueue', () => {
  it('queues user_message rows carrying steer:true in seq order', () => {
    const events = [
      ev(2, 'user_message', { steer: true, text: 'second' }),
      ev(1, 'user_message', { steer: true, text: 'first' }),
    ];
    expect(deriveSteerQueue(events)).toEqual([
      { seq: 2, text: 'second' },
      { seq: 1, text: 'first' },
    ]);
  });

  it('ignores non-steer messages and blank text', () => {
    const events = [
      ev(1, 'user_message', { text: 'plain message' }),
      ev(2, 'user_message', { steer: true, text: '   ' }),
      ev(3, 'user_message', { steer: false, text: 'explicitly unsteered' }),
      ev(4, 'user_message', { park: true, text: 'parked, not steered' }),
      ev(5, 'agent_text', { steer: true, text: 'agent cannot steer' }),
    ];
    expect(deriveSteerQueue(events)).toEqual([]);
  });

  it('drops steers consumed by a run_prompt batch', () => {
    const events = [
      ev(1, 'user_message', { steer: true, text: 'first' }),
      ev(2, 'user_message', { steer: true, text: 'second' }),
      ev(9, 'review_action', { action: 'run_prompt', origin: 'continuation', actor: 'auto_panel', steer_seqs: [1, 2] }),
    ];
    expect(deriveSteerQueue(events)).toEqual([]);
  });

  it('tracks consumption across a chain of continuations', () => {
    const events = [
      ev(1, 'user_message', { steer: true, text: 'one' }),
      ev(2, 'user_message', { steer: true, text: 'two' }),
      ev(3, 'user_message', { steer: true, text: 'three' }),
      ev(9, 'review_action', { action: 'run_prompt', origin: 'continuation', steer_seqs: [1, 2] }),
      ev(10, 'user_message', { steer: true, text: 'four' }),
      ev(20, 'review_action', { action: 'run_prompt', origin: 'continuation', steer_seqs: [3] }),
    ];
    expect(deriveSteerQueue(events)).toEqual([{ seq: 10, text: 'four' }]);
  });

  it('drops steers removed by steer_dropped, single and batch', () => {
    const events = [
      ev(1, 'user_message', { steer: true, text: 'dropped single' }),
      ev(2, 'user_message', { steer: true, text: 'dropped in batch' }),
      ev(3, 'user_message', { steer: true, text: 'also batched' }),
      ev(4, 'user_message', { steer: true, text: 'survivor' }),
      ev(8, 'review_action', { action: 'steer_dropped', steer_seq: 1 }),
      ev(9, 'review_action', { action: 'steer_dropped', steer_seqs: [2, 3], cause: 'cancelled' }),
    ];
    expect(deriveSteerQueue(events)).toEqual([{ seq: 4, text: 'survivor' }]);
  });

  it('ignores the cause field — any steer_dropped closes the seq', () => {
    const events = [
      ev(1, 'user_message', { steer: true, text: 'cancel casualty' }),
      ev(2, 'user_message', { steer: true, text: 'admission casualty' }),
      ev(8, 'review_action', { action: 'steer_dropped', steer_seqs: [1], cause: 'cancelled' }),
      ev(9, 'review_action', { action: 'steer_dropped', steer_seqs: [2], cause: 'concurrency_cap' }),
    ];
    expect(deriveSteerQueue(events)).toEqual([]);
  });

  it('leaves unrelated review actions alone', () => {
    const events = [
      ev(1, 'user_message', { steer: true, text: 'survivor' }),
      ev(7, 'review_action', { action: 'accept', diff_id: 9 }),
      ev(8, 'review_action', { action: 'run_prompt', goal_seqs: [42] }),
    ];
    expect(deriveSteerQueue(events)).toEqual([{ seq: 1, text: 'survivor' }]);
  });
});

describe('deriveActivePrompt', () => {
  it('returns the latest plain send text', () => {
    const events = [
      ev(1, 'user_message', { text: 'first prompt' }),
      ev(2, 'agent_text', { text: 'working' }),
      ev(3, 'user_message', { text: 'latest prompt' }),
      ev(4, 'user_message', { steer: true, text: 'steers never replace the prompt' }),
    ];
    expect(deriveActivePrompt(events)).toBe('latest prompt');
  });

  it('joins the steer texts a continuation consumed', () => {
    const events = [
      ev(1, 'user_message', { text: 'original prompt' }),
      ev(2, 'user_message', { steer: true, text: 'do this too' }),
      ev(3, 'user_message', { steer: true, text: 'and this' }),
      ev(9, 'review_action', { action: 'run_prompt', origin: 'continuation', steer_seqs: [2, 3] }),
    ];
    expect(deriveActivePrompt(events)).toBe('do this too\n\nand this');
  });

  it('joins the parked texts a goal dequeue consumed', () => {
    const events = [
      ev(1, 'user_message', { text: 'original prompt' }),
      ev(2, 'user_message', { park: true, text: 'queued goal' }),
      ev(9, 'review_action', { action: 'run_prompt', origin: 'parked_goal', goal_seqs: [2] }),
    ];
    expect(deriveActivePrompt(events)).toBe('queued goal');
  });

  it('a later plain send supersedes an older continuation receipt', () => {
    const events = [
      ev(1, 'user_message', { steer: true, text: 'drained' }),
      ev(9, 'review_action', { action: 'run_prompt', origin: 'continuation', steer_seqs: [1] }),
      ev(10, 'user_message', { text: 'fresh prompt' }),
    ];
    expect(deriveActivePrompt(events)).toBe('fresh prompt');
  });

  it('tolerates references with no journaled message', () => {
    const events = [
      ev(1, 'user_message', { steer: true, text: 'known steer' }),
      ev(9, 'review_action', { action: 'run_prompt', origin: 'continuation', steer_seqs: [77, 1, 88] }),
    ];
    expect(deriveActivePrompt(events)).toBe('known steer');
    expect(deriveActivePrompt([ev(9, 'review_action', { action: 'run_prompt', steer_seqs: [77] })])).toBeNull();
  });

  it('resolves nothing when the receipt carries no seqs', () => {
    const events = [
      ev(1, 'user_message', { text: 'old prompt' }),
      ev(9, 'review_action', { action: 'run_prompt', origin: 'continuation' }),
    ];
    expect(deriveActivePrompt(events)).toBeNull();
  });

  it('returns null when nothing qualifies', () => {
    expect(deriveActivePrompt([])).toBeNull();
    expect(deriveActivePrompt([
      ev(1, 'agent_text', { text: 'agent chatter' }),
      ev(2, 'user_message', { steer: true, text: 'queued, not started' }),
      ev(3, 'user_message', { park: true, text: 'parked, not started' }),
    ])).toBeNull();
  });

  // Panel diff #9 R2/R3: the retry's real prompt is the dead run's goal
  // PLUS the steers queued at the false stop (daemon: texts = [meta.goal,
  // steerTexts...]) — the card must show the whole assembly, and a
  // steerless retry must still pin a card instead of vanishing.
  it('a retry shows the retried goal joined with the drained steers', () => {
    const events = [
      ev(1, 'user_message', { text: 'original goal' }),
      ev(2, 'user_message', { steer: true, text: 'steered mid-run' }),
      ev(9, 'review_action', { action: 'run_prompt', origin: 'retry', steer_seqs: [2] }),
    ];
    expect(deriveActivePrompt(events)).toBe('original goal\n\nsteered mid-run');
  });

  it('a steerless retry still pins the retried prompt', () => {
    const events = [
      ev(1, 'user_message', { text: 'silent prompt' }),
      ev(9, 'review_action', { action: 'run_prompt', origin: 'retry' }),
    ];
    expect(deriveActivePrompt(events)).toBe('silent prompt');
  });

  it('a retry after a continuation re-derives the continuation prompt as its goal', () => {
    const events = [
      ev(1, 'user_message', { text: 'original goal' }),
      ev(2, 'user_message', { steer: true, text: 'first steer' }),
      ev(8, 'review_action', { action: 'run_prompt', origin: 'continuation', steer_seqs: [2] }),
      ev(3, 'user_message', { steer: true, text: 'steer against the continuation' }),
      ev(9, 'review_action', { action: 'run_prompt', origin: 'retry', steer_seqs: [3] }),
    ];
    // daemon: the continuation run's meta.goal is the joined steers.
    expect(deriveActivePrompt(events)).toBe('first steer\n\nsteer against the continuation');
  });

  it('a retry with an unresolvable predecessor degrades to the steers alone', () => {
    const events = [
      ev(2, 'user_message', { steer: true, text: 'only the steer survives' }),
      ev(9, 'review_action', { action: 'run_prompt', origin: 'retry', steer_seqs: [2] }),
    ];
    expect(deriveActivePrompt(events)).toBe('only the steer survives');
  });

  it('a steerless retry with an unresolvable predecessor resolves nothing', () => {
    const events = [
      ev(9, 'review_action', { action: 'run_prompt', origin: 'retry' }),
    ];
    expect(deriveActivePrompt(events)).toBeNull();
  });
});

describe('latestRunSteerSeqs', () => {
  it('returns the latest run_prompt steer_seqs', () => {
    const events = [
      ev(1, 'user_message', { steer: true, text: 'drained' }),
      ev(9, 'review_action', { action: 'run_prompt', origin: 'continuation', steer_seqs: [1] }),
    ];
    expect(latestRunSteerSeqs(events)).toEqual([1]);
  });

  it('follows a plain send to null so the count tracks the labelled run', () => {
    const events = [
      ev(9, 'review_action', { action: 'run_prompt', origin: 'continuation', steer_seqs: [1, 2] }),
      ev(10, 'user_message', { text: 'fresh prompt' }),
    ];
    expect(latestRunSteerSeqs(events)).toBeNull();
    expect(latestRunSteerSeqs([])).toBeNull();
    expect(latestRunSteerSeqs([
      ev(9, 'review_action', { action: 'run_prompt', goal_seqs: [3] }),
    ])).toBeNull();
  });
});
