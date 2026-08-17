import { describe, expect, it } from 'vitest';

import { deriveParkedGoals } from './parked';
import type { EventPayload, OdoEvent } from './types';

function ev(seq: number, type: OdoEvent['type'], payload: EventPayload = {}): OdoEvent {
  return { id: seq, conversation_id: 1, seq, type, payload, created_at: '2026-08-17T00:00:00.000Z' };
}

describe('deriveParkedGoals', () => {
  it('parks user_message rows carrying park:true in seq order', () => {
    const events = [
      ev(2, 'user_message', { park: true, text: 'second' }),
      ev(1, 'user_message', { park: true, text: 'first' }),
    ];
    expect(deriveParkedGoals(events)).toEqual([
      { seq: 2, text: 'second' },
      { seq: 1, text: 'first' },
    ]);
  });

  it('ignores non-parked messages and blank text', () => {
    const events = [
      ev(1, 'user_message', { text: 'plain message' }),
      ev(2, 'user_message', { park: true, text: '   ' }),
      ev(3, 'user_message', { park: false, text: 'explicitly unparked' }),
      ev(4, 'agent_text', { park: true, text: 'agent cannot park' }),
    ];
    expect(deriveParkedGoals(events)).toEqual([]);
  });

  it('drops goals consumed by a run_prompt', () => {
    const events = [
      ev(1, 'user_message', { park: true, text: 'queued' }),
      ev(5, 'review_action', { action: 'run_prompt', goal_seqs: [1] }),
    ];
    expect(deriveParkedGoals(events)).toEqual([]);
  });

  it('drops goals removed by parked_goal_dropped', () => {
    const events = [
      ev(1, 'user_message', { park: true, text: 'drop me' }),
      ev(2, 'user_message', { park: true, text: 'keep me' }),
      ev(6, 'review_action', { action: 'parked_goal_dropped', goal_seq: 1 }),
    ];
    expect(deriveParkedGoals(events)).toEqual([{ seq: 2, text: 'keep me' }]);
  });

  it('leaves unrelated review actions alone', () => {
    const events = [
      ev(1, 'user_message', { park: true, text: 'survivor' }),
      ev(7, 'review_action', { action: 'accept', diff_id: 9 }),
    ];
    expect(deriveParkedGoals(events)).toEqual([{ seq: 1, text: 'survivor' }]);
  });
});
