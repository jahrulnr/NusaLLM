import { el } from './dom.js';

// Shared lifecycle mark for collapsed thinking and tool cards.
// `live` is in-progress work, `idle` is a recorded call with no outcome,
// `done` finished cleanly, `failed` finished with a tool error.
export const statusOrbStylesheet = new URL('./status-orb.css', import.meta.url).href;

export function statusOrb() {
  const node = el('span', { class: 'status-orb', 'aria-hidden': 'true' }, [
    el('span', { class: 'orb-spin' }),
    el('span', { class: 'orb-core' }),
  ]);
  return {
    node,
    set(phase) {
      if (node.dataset.phase !== phase) node.dataset.phase = phase;
    },
  };
}
