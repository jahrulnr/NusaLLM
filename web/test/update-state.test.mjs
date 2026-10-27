import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  SKIPPED_KEY, NOTIFIED_KEY, skipVersion, skippedVersion, markNotified, notifiedVersion,
  visibleUpdate, isFreshNotice, ago, releasedOn, releaseBody,
} from '../lib/update-state.js';

// Storage is a Map here; the app passes localStorage, which has the same two methods.
const memory = () => {
  const data = new Map();
  return {
    getItem: (key) => (data.has(key) ? data.get(key) : null),
    setItem: (key, value) => data.set(key, String(value)),
  };
};

const available = {
  enabled: true, available: true, current: '0.10.3', latest: '0.10.4', installable: true,
};

test('the header shows only a newer release in a release build', () => {
  const s = memory();
  assert.equal(visibleUpdate(null, s), null);
  assert.equal(visibleUpdate({ ...available, enabled: false }, s), null, 'dev builds never offer updates');
  assert.equal(visibleUpdate({ ...available, available: false }, s), null, 'up to date');
  assert.equal(visibleUpdate({ enabled: true, available: true, latest: '' }, s), null, 'no release named');
  assert.equal(visibleUpdate(available, s), available);
});

test('skipping hides exactly that release, and the next release shows again', () => {
  const s = memory();
  skipVersion(s, '0.10.4');
  assert.equal(skippedVersion(s), '0.10.4');
  assert.equal(visibleUpdate(available, s), null);
  assert.equal(visibleUpdate({ ...available, latest: '0.10.5' }, s).latest, '0.10.5');
  skipVersion(s, ''); // "Show again" clears the skip
  assert.equal(visibleUpdate(available, s), available);
});

test('a fresh notice fires once per release and never for a skipped one', () => {
  const s = memory();
  assert.equal(isFreshNotice(available, s), true);
  markNotified(s, '0.10.4');
  assert.equal(notifiedVersion(s), '0.10.4');
  assert.equal(isFreshNotice(available, s), false);
  assert.equal(isFreshNotice({ ...available, latest: '0.10.5' }, s), true);

  const skipped = memory();
  skipVersion(skipped, '0.10.4');
  assert.equal(isFreshNotice(available, skipped), false);
});

test('storage that throws degrades to no memory without breaking the rules', () => {
  const denied = {
    getItem() { throw new Error('denied'); },
    setItem() { throw new Error('denied'); },
  };
  assert.equal(skippedVersion(denied), '');
  assert.doesNotThrow(() => markNotified(denied, '0.10.4'));
  assert.equal(visibleUpdate(available, denied), available);
  assert.equal(SKIPPED_KEY, 'nusallm.update.skipped');
  assert.equal(NOTIFIED_KEY, 'nusallm.update.notified');
});

test('ago reads the elapsed time at a human scale', () => {
  const now = Date.parse('2026-10-27T12:00:00Z');
  assert.equal(ago('2026-10-27T11:59:40Z', now), 'just now');
  assert.equal(ago('2026-10-27T11:48:00Z', now), '12 min ago');
  assert.equal(ago('2026-10-27T09:00:00Z', now), '3 h ago');
  assert.equal(ago('2026-10-25T12:00:00Z', now), '2 d ago');
  assert.equal(ago('', now), '');
  assert.equal(ago('not a date', now), '');
});

test('releasedOn is empty without a usable date', () => {
  assert.equal(releasedOn(''), '');
  assert.equal(releasedOn('not a date'), '');
  assert.notEqual(releasedOn('2026-10-28T00:00:00Z'), '');
});

test('the release body drops its own version heading', () => {
  assert.equal(
    releaseBody('## [0.10.4] - 2026-10-27\n\n### Fixed\n- one'),
    '### Fixed\n- one',
  );
  assert.equal(releaseBody('### Fixed\n- one'), '### Fixed\n- one');
  assert.equal(releaseBody(''), '');
  assert.equal(releaseBody(undefined), '');
});
