import '@testing-library/jest-dom/vitest';

// React's act() warns unless this flag is set in the test environment.
declare global {
  // eslint-disable-next-line no-var
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined;
}
globalThis.IS_REACT_ACT_ENVIRONMENT = true;

// localStorage shim (tri-review P1 #5 component tests, 2026-08-24): under
// vitest 4 + jsdom 30 on Node ≥22, window===globalThis and `localStorage`
// resolves to NODE's experimental global accessor, which is undefined
// without --localstorage-file — components reading odo-* persisted keys at
// mount (App, Sidebar, SettingsPanel) would crash. Install an in-memory
// Storage ONLY when the current binding is unusable, so a future fixed
// jsdom global wins by default.
class InMemoryStorage implements Storage {
  private map = new Map<string, string>();
  get length(): number {
    return this.map.size;
  }
  clear(): void {
    this.map.clear();
  }
  getItem(key: string): string | null {
    return this.map.get(key) ?? null;
  }
  key(index: number): string | null {
    return [...this.map.keys()][index] ?? null;
  }
  removeItem(key: string): void {
    this.map.delete(key);
  }
  setItem(key: string, value: string): void {
    this.map.set(key, String(value));
  }
}

if (typeof globalThis.localStorage?.getItem !== "function") {
  Object.defineProperty(globalThis, "localStorage", {
    value: new InMemoryStorage(),
    configurable: true,
    writable: true,
  });
}
