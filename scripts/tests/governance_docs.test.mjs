import assert from 'node:assert/strict';
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const runbookRoot = join(repoRoot, 'runbooks');

test('every Runbook is indexed', () => {
  const index = readFileSync(join(runbookRoot, 'README.md'), 'utf8');
  const indexed = new Set([...index.matchAll(/\]\(([^)#]+\.md)(?:#[^)]+)?\)/g)].map((match) => match[1]));
  const runbooks = readdirSync(runbookRoot).filter((name) => name.endsWith('.md') && name !== 'README.md');
  assert.deepEqual(runbooks.filter((name) => !indexed.has(name)), []);
});

test('relative Markdown links resolve to existing files', () => {
  const failures = [];
  for (const name of readdirSync(runbookRoot).filter((item) => item.endsWith('.md'))) {
    const source = readFileSync(join(runbookRoot, name), 'utf8');
    for (const match of source.matchAll(/\[[^\]]+\]\(([^)#]+)(?:#[^)]+)?\)/g)) {
      const target = match[1];
      if (/^(?:https?:|mailto:)/.test(target)) continue;
      if (!existsSync(resolve(runbookRoot, target))) failures.push(`${name}: ${target}`);
    }
  }
  assert.deepEqual(failures, []);
});
