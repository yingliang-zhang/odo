import { describe, expect, it } from 'vitest';

import { deriveTodoState, visibleTodoItems } from './todo';
import type { EventPayload, OdoEvent } from './types';

function ev(seq: number, type: OdoEvent['type'], payload: EventPayload = {}): OdoEvent {
  return { id: seq, conversation_id: 1, seq, type, payload, created_at: '2026-08-17T00:00:00.000Z' };
}

const snapshot = (items: { id: string; text: string; status?: string; origin_seq?: number; updated_seq: number }[]): EventPayload => ({
  action: 'todo_merge',
  snapshot: items.map((it) => ({
    id: it.id,
    text: it.text,
    status: it.status ?? 'open',
    origin_seq: it.origin_seq ?? 0,
    updated_seq: it.updated_seq,
  })),
});

describe('deriveTodoState', () => {
  it('returns empty without a todo_merge snapshot', () => {
    expect(deriveTodoState([ev(1, 'user_message', { text: 'hi' })])).toEqual([]);
  });

  it('uses the newest snapshot, sorted by numeric id', () => {
    const events = [
      ev(2, 'review_action', snapshot([{ id: 't1', text: 'old', updated_seq: 2 }])),
      ev(5, 'review_action', snapshot([
        { id: 't10', text: 'ten', updated_seq: 5 },
        { id: 't2', text: 'two', updated_seq: 5 },
      ])),
    ];
    const items = deriveTodoState(events);
    expect(items.map((i) => [i.id, i.text])).toEqual([
      ['t2', 'two'],
      ['t10', 'ten'],
    ]);
    expect(items.every((i) => i.status === 'open' && !i.stale && !i.swept)).toBe(true);
  });

  it('marks closed items swept once the fold boundary passes updated_seq', () => {
    const events = [
      ev(5, 'review_action', snapshot([{ id: 't1', text: 'done thing', status: 'done', updated_seq: 4 }])),
      ev(8, 'review_action', { action: 'distill', last_seq: 10 }),
    ];
    const items = deriveTodoState(events);
    expect(items[0].swept).toBe(true);
  });

  it('marks open items stale after three folds without an update', () => {
    const events = [
      ev(5, 'review_action', snapshot([
        { id: 't1', text: 'stale', updated_seq: 5 },
        { id: 't2', text: 'touched', updated_seq: 25 },
      ])),
      ev(10, 'review_action', { action: 'distill' }),
      ev(20, 'review_action', { action: 'distill' }),
      ev(30, 'review_action', { action: 'distill' }),
    ];
    const items = deriveTodoState(events);
    expect(items[0].stale).toBe(true);
    expect(items[1].stale).toBe(false);
  });
});

describe('visibleTodoItems', () => {
  it('lists unswept items open-first', () => {
    const items = deriveTodoState([
      ev(5, 'review_action', snapshot([
        { id: 't1', text: 'closed', status: 'done', updated_seq: 5 },
        { id: 't2', text: 'open', updated_seq: 5 },
        { id: 't3', text: 'swept', status: 'done', updated_seq: 1 },
      ])),
      ev(9, 'review_action', { action: 'distill', last_seq: 2 }),
    ]);
    expect(visibleTodoItems(items).map((i) => i.id)).toEqual(['t2', 't1']);
  });
});
