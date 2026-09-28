// Tool cards have no separate failure status. A settled call is failed
// when its visible output is one of the tool-result markers the turn
// already stores: an `error:` result, a user interrupt, a nonzero exit,
// or a signal death. Live statuses stay `live` even if a chunk already
// contains one of those strings.
export function toolOrbPhase(status, output) {
  if (status === 'requested' || status === 'streaming') return 'live';
  if (status !== 'settled') return 'idle';
  return toolOutputFailed(output) ? 'failed' : 'done';
}

export function toolOutputFailed(output) {
  const text = String(output ?? '').trim();
  if (!text) return false;
  if (text === 'interrupted by user') return true;
  if (text.startsWith('error:')) return true;
  if (/\(exit [1-9]\d*\)$/.test(text)) return true;
  if (/\(signal: [^)]+\)$/.test(text)) return true;
  return false;
}
