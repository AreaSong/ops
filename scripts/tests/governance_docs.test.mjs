import assert from 'node:assert/strict';
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const runbookRoot = join(repoRoot, 'runbooks');
const runbookSubdirs = ['playbooks', 'records'];

test('every Runbook is indexed', () => {
  const index = readFileSync(join(runbookRoot, 'README.md'), 'utf8');
  const indexed = new Set([...index.matchAll(/\]\(([^)#]+\.md)(?:#[^)]+)?\)/g)].map((match) => match[1]));
  const expected = readdirSync(runbookRoot).filter((name) => name.endsWith('.md') && name !== 'README.md');
  for (const sub of runbookSubdirs) {
    for (const name of readdirSync(join(runbookRoot, sub)).filter((item) => item.endsWith('.md'))) {
      expected.push(`${sub}/${name}`);
    }
  }
  assert.deepEqual(expected.filter((path) => !indexed.has(path)), []);
});

test('relative Markdown links resolve to existing files', () => {
  const failures = [];
  const dirs = [runbookRoot, ...runbookSubdirs.map((sub) => join(runbookRoot, sub))];
  for (const dir of dirs) {
    for (const name of readdirSync(dir).filter((item) => item.endsWith('.md'))) {
      const source = readFileSync(join(dir, name), 'utf8');
      for (const match of source.matchAll(/\[[^\]]+\]\(([^)#]+)(?:#[^)]+)?\)/g)) {
        const target = match[1];
        if (/^(?:https?:|mailto:)/.test(target)) continue;
        if (!existsSync(resolve(dir, target))) failures.push(`${join(dir, name)}: ${target}`);
      }
    }
  }
  assert.deepEqual(failures, []);
});
