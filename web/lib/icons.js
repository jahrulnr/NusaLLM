// Minimal inline SVG icon set — built with createElementNS only, no
// icon font, no innerHTML. Paths are 24-viewBox strokes.

const NS = 'http://www.w3.org/2000/svg';

const PATHS = {
  menu: 'M4 7h16M4 12h16M4 17h16',
  x: 'M6 6l12 12M18 6L6 18',
  plus: 'M12 5v14M5 12h14',
  'arrow-up': 'M12 19V5m-6 6 6-6 6 6',
  stop: 'M7 7h10v10H7z',
  chevron: 'M9 6l6 6-6 6',
  trash: 'M4 7h16M9 7V5h6v2M6 7l1 13h10l1-13',
  pencil: 'M4 20l1-4L16.5 4.5l3 3L8 19l-4 1z',
  dot: 'M12 8a4 4 0 100 8 4 4 0 000-8z',
  gear: 'M12 9a3 3 0 100 6 3 3 0 000-6zM19 12a7 7 0 01-.2 1.6l2 1.6-2 3.4-2.4-.9a7 7 0 01-1.4.8l-.4 2.5h-4l-.4-2.5a7 7 0 01-1.4-.8l-2.4.9-2-3.4 2-1.6A7 7 0 015 12c0-.5.1-1.1.2-1.6l-2-1.6 2-3.4 2.4.9c.4-.3.9-.6 1.4-.8l.4-2.5h4l.4 2.5c.5.2 1 .5 1.4.8l2.4-.9 2 3.4-2 1.6c.1.5.2 1.1.2 1.6z',
  plug: 'M9 3v5m6-5v5M7 8h10v4a5 5 0 01-5 5 5 5 0 01-5-5V8zm5 9v4',
  tool: 'M14.5 6.5a4 4 0 00-5.6 5L4 16.4V20h3.6l4.9-4.9a4 4 0 005-5.6l-2.8 2.8-2.6-2.6 2.4-3.2z',
  panel: 'M4 5h16v14H4zM15 5v14',
  folder: 'M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2V7z',
  // Zero-length segments render as dots under the round caps.
  more: 'M5 12h.01M12 12h.01M19 12h.01',
  // Arrows folding inward: summarize the thread.
  compact: 'M4 14h6v6M20 10h-6V4M14 10l7-7M3 21l7-7',
};

export function icon(name, size = 14) {
  const svg = document.createElementNS(NS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('width', String(size));
  svg.setAttribute('height', String(size));
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('fill', 'none');
  svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '1.8');
  svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round');
  const path = document.createElementNS(NS, 'path');
  path.setAttribute('d', PATHS[name] ?? PATHS.dot);
  svg.append(path);
  return svg;
}
