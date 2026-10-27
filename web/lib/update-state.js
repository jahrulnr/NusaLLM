// Update-notice rules, kept free of DOM so they test under node. The server
// decides whether a release is newer (`available`); this module only decides
// what the browser shows about it and remembers what the person already dealt
// with. Storage is passed in, so tests use a Map and the app uses localStorage.

export const SKIPPED_KEY = 'nusallm.update.skipped';
export const NOTIFIED_KEY = 'nusallm.update.notified';

// Storage can be absent or throw (private modes, disabled storage). Every
// access is guarded and degrades to "no memory this session".
export function readKey(storage, key) {
  try {
    return (storage && storage.getItem(key)) || '';
  } catch {
    return '';
  }
}

export function writeKey(storage, key, value) {
  try {
    if (storage) storage.setItem(key, value);
  } catch {
    /* no memory this session */
  }
}

// The version the person chose to skip. Skipping hides the header pill and
// the toast for that release only; the next release shows up again.
export function skippedVersion(storage) {
  return readKey(storage, SKIPPED_KEY);
}

export function skipVersion(storage, version) {
  writeKey(storage, SKIPPED_KEY, version);
}

// The newest version the toast already announced in this browser.
export function notifiedVersion(storage) {
  return readKey(storage, NOTIFIED_KEY);
}

export function markNotified(storage, version) {
  writeKey(storage, NOTIFIED_KEY, version);
}

// The status the header pill and the dialog show: null unless an update
// exists and the person has not skipped that exact version.
export function visibleUpdate(info, storage) {
  if (!info || !info.enabled || !info.available || !info.latest) return null;
  return skippedVersion(storage) === info.latest ? null : info;
}

// True once per release: a visible update for a version this browser has not
// announced yet. Skipped versions never announce.
export function isFreshNotice(info, storage) {
  return visibleUpdate(info, storage) !== null && notifiedVersion(storage) !== info.latest;
}

// Relative time for "last checked": "just now", "12 min ago", "3 h ago",
// "2 d ago". Older than two weeks reads as a date.
export function ago(iso, now = Date.now()) {
  if (!iso) return '';
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return '';
  const mins = Math.round((now - at) / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins} min ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours} h ago`;
  const days = Math.round(hours / 24);
  if (days < 14) return `${days} d ago`;
  return new Date(at).toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });
}

// "Oct 28, 2026" for a release date; empty when the server had none.
export function releasedOn(iso) {
  if (!iso) return '';
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return '';
  return new Date(at).toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });
}

// The release body without its leading "## [x.y.z] - date" heading: the
// dialog already shows the version in its title, so repeating it reads twice.
export function releaseBody(notes) {
  return String(notes || '').replace(/^##\s*\[[^\]]*\][^\n]*\n+/, '').trim();
}
