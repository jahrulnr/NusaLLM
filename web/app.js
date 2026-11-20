// NusaLLM WebUI entry: theme, layout shell, and boot.
import { html, render, useEffect } from '/vendor/preact/htm-preact.mjs';
import { useStore, state, emit } from './lib/store.js';
import { start } from './lib/chat.js';
import { Sidebar } from './components/sidebar.js';
import { ChatView, HomeView } from './components/chat.js';
import { SettingsView } from './components/settings.js';
import { Toasts } from './components/ui.js';

const dark = window.matchMedia('(prefers-color-scheme: dark)');
function applyTheme() {
  const t = state.prefs.theme;
  document.documentElement.dataset.theme = t === 'system' ? (dark.matches ? 'dark' : 'light') : t;
}
dark.addEventListener('change', applyTheme);

function App() {
  const s = useStore();
  useEffect(applyTheme, [s.prefs.theme]);
  const view = s.route.view === 'chat'
    ? html`<${ChatView} key=${s.route.id} id=${s.route.id} />`
    : s.route.view === 'settings' ? html`<${SettingsView} />` : html`<${HomeView} />`;
  const close = () => { state.sidebarOpen = false; emit(); };
  return html`<div class="flex h-full">
    <aside class="hidden md:flex w-[264px] shrink-0 border-r border-line">
      <${Sidebar} />
    </aside>
    ${s.sidebarOpen && html`<div class="md:hidden fixed inset-0 z-40 flex" onKeyDown=${(e) => e.key === 'Escape' && close()}>
      <div class="w-[min(300px,86vw)] h-full border-r border-line shadow-pop"><${Sidebar} /></div>
      <button class="flex-1 bg-black/50" aria-label="Close threads" onClick=${close}></button>
    </div>`}
    <main class="flex-1 min-w-0 h-full">${view}</main>
    <${Toasts} />
  </div>`;
}

applyTheme();
start();
const root = document.getElementById('app');
root.textContent = '';
render(html`<${App} />`, root);
