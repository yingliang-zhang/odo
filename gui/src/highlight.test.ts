import { describe, expect, it } from 'vitest';

import { languageFromPath, tokenize } from './highlight';

describe('languageFromPath', () => {
  it('maps extensions case-insensitively and rejects unknowns', () => {
    expect(languageFromPath('main.go')).toBe('go');
    expect(languageFromPath('a/b/Comp.TSX')).toBe('ts');
    expect(languageFromPath('stub.pyi')).toBe('python');
    expect(languageFromPath('env.zsh')).toBe('bash');
    expect(languageFromPath('cfg.yml')).toBe('yaml');
    expect(languageFromPath('data.json')).toBe('json');
    expect(languageFromPath('Makefile')).toBeNull();
    expect(languageFromPath('archive.tar.gz')).toBeNull();
  });
});

describe('tokenize', () => {
  it('passes through unstyled when no language or empty', () => {
    expect(tokenize('anything', null)).toEqual([{ text: 'anything', cls: null }]);
    expect(tokenize('', 'go')).toEqual([{ text: '', cls: null }]);
  });

  it('classifies go keywords, calls, and comments', () => {
    expect(tokenize('func main() { // entry', 'go')).toEqual([
      { text: 'func', cls: 'tok-keyword' },
      { text: ' ', cls: null },
      { text: 'main', cls: 'tok-fn' },
      { text: '() { ', cls: null },
      { text: '// entry', cls: 'tok-comment' },
    ]);
  });

  it('classifies ts strings and numbers', () => {
    expect(tokenize('const x = "hi";', 'ts')).toEqual([
      { text: 'const', cls: 'tok-keyword' },
      { text: ' x = ', cls: null },
      { text: '"hi"', cls: 'tok-string' },
      { text: ';', cls: null },
    ]);
    expect(tokenize('0xFF', 'ts')).toEqual([{ text: '0xFF', cls: 'tok-number' }]);
  });

  it('classifies python triple quotes and def', () => {
    expect(tokenize('"""doc"""', 'python')).toEqual([{ text: '"""doc"""', cls: 'tok-string' }]);
    const toks = tokenize('def f(): # note', 'python');
    expect(toks[0]).toEqual({ text: 'def', cls: 'tok-keyword' });
    expect(toks.find((t) => t.text === 'f')?.cls).toBe('tok-fn');
    expect(toks[toks.length - 1]).toEqual({ text: '# note', cls: 'tok-comment' });
  });

  it('lets comments beat strings', () => {
    expect(tokenize('// "not a string"', 'go')).toEqual([
      { text: '// "not a string"', cls: 'tok-comment' },
    ]);
  });

  it('treats json booleans as numbers with no keywords', () => {
    expect(tokenize('true', 'json')).toEqual([{ text: 'true', cls: 'tok-number' }]);
  });
});
