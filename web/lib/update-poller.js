// Polls the update status and re-checks on demand. The server caches the
// release lookup, so polling is cheap: each open browser asks every
// POLL_INTERVAL_MS, and the server answers from its cache or runs one fresh
// lookup per its own TTL. A failed request keeps the last picture and records
// the error next to it, so a flaky network never hides a known update.

export const POLL_INTERVAL_MS = 15 * 60 * 1000;

export class UpdatePoller {
  constructor({
    fetchStatus,
    checkNow,
    onStatus,
    intervalMs = POLL_INTERVAL_MS,
    timers = globalThis,
  }) {
    this.fetchStatus = fetchStatus;
    this.checkNow = checkNow;
    this.onStatus = onStatus;
    this.intervalMs = intervalMs;
    this.timers = timers;
    this.timer = null;
    this.status = null;
    this._seq = 0;
  }

  // Refreshes now, then on every interval until stop().
  start() {
    this.stop();
    const handle = this.timers.setInterval(() => { void this.refresh(); }, this.intervalMs);
    // A Node timer keeps the process alive, so a poll must never be the reason
    // a test run or an embedder hangs. Browsers hand back a number and skip this.
    if (handle && typeof handle.unref === 'function') handle.unref();
    this.timer = handle;
    return this.refresh();
  }

  stop() {
    if (this.timer !== null) {
      this.timers.clearInterval(this.timer);
      this.timer = null;
    }
  }

  // A background poll: the server answers from its cache when it can.
  refresh() {
    return this._apply(() => this.fetchStatus());
  }

  // A manual check: the server re-runs the release lookup now.
  check() {
    return this._apply(() => this.checkNow());
  }

  async _apply(load) {
    const seq = ++this._seq;
    let next;
    try {
      next = await load();
    } catch (err) {
      next = { ...(this.status || {}), error: (err && err.message) || 'request failed' };
    }
    // A stale request landing after a newer one was issued would
    // resurrect an old release picture — only the latest request wins.
    if (seq !== this._seq) return this.status;
    this.status = next;
    this.onStatus(next);
    return next;
  }
}
