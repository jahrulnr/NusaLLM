import { Widget, el, clear } from '../../lib/dom.js';
import { icon } from '../../lib/icons.js';

// Transient notifications: push({tone,title,message}), polite live
// region, auto-expiry, manual dismiss. Tones: info|warn|error.
class ToastStack extends Widget {
  static stylesheet = new URL('./toast-stack.css', import.meta.url);

  constructor() {
    super();
    this.root.classList.add('stack');
    this.root.setAttribute('aria-live', 'polite');
    this.root.setAttribute('role', 'status');
  }

  push({ tone = 'info', title = '', message = '' } = {}) {
    const toast = el('div', { class: `toast ${tone}`, role: 'alert' }, [
      el('div', { class: 'toast-body' }, [
        title ? el('div', { class: 'toast-title', text: title }) : null,
        el('div', { class: 'toast-message', text: message }),
      ]),
      el('button', {
        class: 'toast-close',
        type: 'button',
        'aria-label': 'Dismiss notification',
        onclick: () => toast.remove(),
      }, [icon('x', 12)]),
    ]);
    this.root.append(toast);
    setTimeout(() => { if (toast.isConnected) toast.classList.add('show'); }, 10);
    setTimeout(() => {
      toast.classList.remove('show');
      setTimeout(() => toast.remove(), 200);
    }, 6000);
  }

  clear() { clear(this.root); }
}

customElements.define('nusa-toast-stack', ToastStack);
