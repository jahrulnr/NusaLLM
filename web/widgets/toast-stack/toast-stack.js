import { Widget, el, clear, deepActiveElement, nodeWithin } from '../../lib/dom.js';
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
    // Programmatic only: expiry moves focus here before a focused toast
    // is removed. -1 keeps the stack out of the tab order.
    this.root.setAttribute('tabindex', '-1');
  }

  // action: { label, onClick } adds one text button under the message. It
  // dismisses the toast and then runs onClick.
  push({ tone = 'info', title = '', message = '', action = null } = {}) {
    const toast = el('div', { class: `toast ${tone}`, role: 'alert' }, [
      el('div', { class: 'toast-body' }, [
        title ? el('div', { class: 'toast-title', text: title }) : null,
        el('div', { class: 'toast-message', text: message }),
        action ? el('button', {
          class: 'toast-action', type: 'button',
          onclick: () => { this._dismiss(toast); action.onClick(); },
        }, [el('span', { text: action.label })]) : null,
      ]),
      el('button', {
        class: 'toast-close',
        type: 'button',
        'aria-label': 'Dismiss notification',
        onclick: () => this._dismiss(toast),
      }, [icon('x', 12)]),
    ]);
    this.root.append(toast);
    setTimeout(() => { if (toast.isConnected) toast.classList.add('show'); }, 10);
    setTimeout(() => {
      toast.classList.remove('show');
      setTimeout(() => this._dismiss(toast), 200);
    }, 6000);
  }

  // Removing the focused control strands keyboard focus on a detached
  // node. The stack is programmatically focusable and stays connected.
  _dismiss(toast) {
    if (nodeWithin(toast, deepActiveElement()) && this.root?.isConnected) {
      this.root.focus?.();
    }
    toast.remove();
  }

  clear() { clear(this.root); }
}

customElements.define('nusa-toast-stack', ToastStack);
