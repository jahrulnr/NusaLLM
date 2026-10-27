// Pure rules for the in-app update: which steps are done, which is active, and
// what the popup says. The server owns the install. These functions only turn
// its progress into what a person reads, so they test without a DOM.

export const STEPS = [
  { id: 'download', label: 'Download' },
  { id: 'verify', label: 'Verify checksum' },
  { id: 'install', label: 'Install' },
  { id: 'restart', label: 'Restart' },
];

const ORDER = STEPS.map((s) => s.id);

// Each step is done, active, pending, or failed. A view flagged reconnecting
// is past the install: the server has dropped the connection to replace its
// own process, so the restart step stays active until the new one answers.
export function stepStates(view) {
  const state = view.state || 'idle';
  if (state === 'succeeded') return STEPS.map((s) => ({ ...s, state: 'done' }));
  const current = view.reconnecting ? 'restart' : view.phase;
  const at = ORDER.indexOf(current);
  return STEPS.map((s, i) => {
    let mark = 'pending';
    if (state === 'failed') {
      if (i < at) mark = 'done';
      else if (i === at) mark = 'failed';
    } else if (state === 'running') {
      if (i < at) mark = 'done';
      else if (i === at) mark = 'active';
    }
    return { ...s, state: mark };
  });
}

// Download progress as a whole percentage, or null when there is no byte count
// to show. The count is clamped, so a late report never overshoots 100.
export function percent(view) {
  const total = Number(view.bytesTotal) || 0;
  if (view.phase !== 'download' || total <= 0) return null;
  const done = Math.min(Number(view.bytesDone) || 0, total);
  return Math.floor((done * 100) / total);
}

// "512 KB", "28.3 MB": the scale a person reads at a glance.
export function sizeLabel(bytes) {
  const n = Math.max(0, Number(bytes) || 0);
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

// The popup's headline, as the install moves through its steps.
export function headline(view) {
  const v = view.version || '';
  if (view.state === 'succeeded') return `NusaLLM ${v} is running`;
  if (view.state === 'failed') return `The update to ${v} did not finish`;
  if (view.state === 'running') {
    if (view.reconnecting || view.phase === 'restart') return restartTitle(view);
    if (view.phase === 'verify') return `Verifying NusaLLM ${v}`;
    if (view.phase === 'install') return `Installing NusaLLM ${v}`;
    return `Downloading NusaLLM ${v}`;
  }
  return v ? `Update NusaLLM to ${v}` : 'Update NusaLLM';
}

function restartTitle(view) {
  return view.restart === 'process' ? 'Restarting NusaLLM' : 'Restarting the service';
}

// Which way the restart goes, in words a person can act on.
export function restartNote(view) {
  return `${restartTitle(view)}. This window reconnects when it is back.`;
}
