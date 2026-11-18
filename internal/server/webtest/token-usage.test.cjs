/* Unit tests for the pure cache-hit overlay path builder in token-usage.js.
   The module itself is DOM-bound, so the function body is extracted from the
   shipped source to keep the test honest about what actually runs. */
'use strict';
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

function loadCacheHitPath() {
  const source = fs.readFileSync(path.join(__dirname, '..', 'web', 'token-usage.js'), 'utf8');
  const match = source.match(/function cacheHitPath\(hits, x, y\) \{[\s\S]*?\n  \}/);
  assert.ok(match, 'cacheHitPath helper not found in token-usage.js');
  return new Function('return ' + match[0])();
}

const x = i => i * 10;
const y = v => 100 - v;

test('cacheHitPath skips null buckets without breaking the line', () => {
  const cacheHitPath = loadCacheHitPath();
  const { d, dot } = cacheHitPath([null, 20, null, 40, null], x, y);
  assert.equal(d, 'M10,80L30,60');
  assert.equal(dot, null);
});

test('cacheHitPath connects known buckets across gaps in both directions', () => {
  const cacheHitPath = loadCacheHitPath();
  const { d, dot } = cacheHitPath([50, null, null, 80, 10], x, y);
  assert.equal(d, 'M0,50L30,20L40,90');
  assert.equal(dot, null);
});

test('cacheHitPath returns a dot for a single known bucket', () => {
  const cacheHitPath = loadCacheHitPath();
  const { d, dot } = cacheHitPath([null, 50, null], x, y);
  assert.equal(d, 'M10,50');
  assert.deepEqual(dot, { cx: 10, cy: 50 });
});

test('cacheHitPath returns an empty path when every bucket is null', () => {
  const cacheHitPath = loadCacheHitPath();
  const { d, dot } = cacheHitPath([null, null, null], x, y);
  assert.equal(d, '');
  assert.equal(dot, null);
});

test('cacheHitPath ignores undefined and non-finite buckets like null', () => {
  const cacheHitPath = loadCacheHitPath();
  const { d, dot } = cacheHitPath([undefined, NaN, 30, Infinity], x, y);
  assert.equal(d, 'M20,70');
  assert.deepEqual(dot, { cx: 20, cy: 70 });
});
