// Drives one in-app update from the browser. The server downloads, verifies,
// installs, and then replaces its own process to run the new version, so a
// dropped connection during the restart is expected. The installer reads that
// drop as the restart rather than a failure, and waits for the new version to
// answer. Timers, clock, and API are injected, so the rules test without a
// network or a DOM.

export const PROGRESS_MS = 400;
export const RESTART_DEADLINE_MS = 90_000;

// Job states the server reports while an install still needs following. A
// "succeeded" job is one whose restart is queued, not yet answered.
const IN_FLIGHT = new Set(['running', 'succeeded']);

export class UpdateInstaller {
  constructor({
    api,
    onChange,
    timers = globalThis,
    now = () => Date.now(),
    pollMs = PROGRESS_MS,
    deadlineMs = RESTART_DEADLINE_MS,
  }) {
    this.api = api;
    this.onChange = onChange;
    this.timers = timers;
    this.now = now;
    this.pollMs = pollMs;
    this.deadlineMs = deadlineMs;
    this.view = { state: 'idle' };
    this.target = '';
    this.timer = null;
    this.restartAt = 0;
  }

  // Starts an install of `version`. A refusal because one is already running
  // on the server turns into following that run instead.
  async begin(version) {
    this.stop();
    this.target = version;
    this.restartAt = 0;
    this._set({ state: 'running', phase: 'download', version, reconnecting: false, error: '' });
    try {
      this._apply(await this.api.startUpdate());
    } catch (err) {
      if (err && err.code === 'update_busy') return this.resume();
      this._finish('failed', messageOf(err));
      return this.view;
    }
    if (this.view.state === 'running') this._schedule();
    return this.view;
  }

  // Follows an install the server is still running, e.g. after a reload. Only
  // an in-flight job is followed: an idle server, a finished or failed job from
  // an earlier page, or a reply that names no state all leave the view alone.
  async resume() {
    this.stop();
    this.restartAt = 0;
    try {
      const progress = await this.api.getUpdateProgress();
      if (!IN_FLIGHT.has(progress.state)) return this.view;
      this.target = progress.version || this.target;
      this._apply(progress);
    } catch {
      return this.view; // nothing to follow: the server is not answering
    }
    if (this.view.state === 'running') this._schedule();
    return this.view;
  }

  stop() {
    if (this.timer !== null) {
      this.timers.clearTimeout(this.timer);
      this.timer = null;
    }
  }

  _schedule() {
    this.stop();
    this.timer = this.timers.setTimeout(() => { void this._tick(); }, this.pollMs);
  }

  async _tick() {
    this.timer = null;
    if (this.view.state !== 'running') return;
    try {
      const progress = await this.api.getUpdateProgress();
      if (progress.state === 'idle') {
        // The server answers with no job: the new process is up. Confirm it runs the target.
        this._set({ phase: 'restart', reconnecting: true });
        await this._awaitVersion();
      } else {
        this._apply(progress);
      }
    } catch {
      // The connection dropped: the server is replacing its process.
      this._set({ phase: 'restart', reconnecting: true });
      await this._awaitVersion();
    }
    if (this.view.state === 'running') this._schedule();
  }

  // Asks for the status and finishes once it reports the target as current. The
  // restart window is bounded from the first moment the server went quiet.
  async _awaitVersion() {
    if (!this.restartAt) this.restartAt = this.now();
    try {
      const info = await this.api.getUpdate();
      if (info.current === this.target) {
        this._finish('succeeded');
        return;
      }
    } catch {
      /* still down: keep waiting */
    }
    if (this.now() - this.restartAt > this.deadlineMs) {
      this._finish('failed', `NusaLLM did not come back on ${this.target} in time. Start it again to run the new version.`);
    }
  }

  // A server progress report becomes the view. A "succeeded" report means the
  // restart was queued, not that the new version runs, so the view stays in its
  // restart step until the new process answers.
  _apply(progress) {
    if (progress.state === 'failed') {
      this._finish('failed', progress.error || 'the update failed');
      return;
    }
    const restarting = progress.state === 'succeeded';
    this._set({
      state: 'running',
      phase: restarting ? 'restart' : progress.phase || 'download',
      version: progress.version || this.target,
      bytesDone: progress.bytesDone || 0,
      bytesTotal: progress.bytesTotal || 0,
      restart: progress.restart || this.view.restart || '',
      error: '',
      reconnecting: false,
    });
  }

  _finish(state, error = '') {
    this.stop();
    const patch = { state, reconnecting: false, error };
    if (state === 'succeeded') {
      patch.phase = 'restart';
      patch.version = this.target;
    }
    this._set(patch);
  }

  _set(patch) {
    this.view = { ...this.view, ...patch };
    this.onChange(this.view);
  }
}

function messageOf(err) {
  return (err && (err.detail || err.message)) || 'request failed';
}
