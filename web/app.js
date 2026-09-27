// Boot module: registering the widget definitions is the entire job —
// app-shell owns orchestration from there.
//
// INVARIANT: app-shell must be imported LAST. Custom elements upgrade
// synchronously at definition time — if app-shell loaded first,
// <nusa-app> would upgrade and assign properties (e.g. sidebar.open)
// onto still-unregistered leaf elements as own expandos that shadow
// the real setters once the leaves finally define.
import './widgets/nav-sidebar/nav-sidebar.js';
import './widgets/conversation-header/conversation-header.js';
import './widgets/message-thread/message-thread.js';
import './widgets/chat-composer/chat-composer.js';
import './widgets/model-picker/model-picker.js';
import './widgets/context-panel/context-panel.js';
import './widgets/provider-manager/provider-manager.js';
import './widgets/settings-editor/settings-editor.js';
import './widgets/tool-catalog/tool-catalog.js';
import './widgets/toast-stack/toast-stack.js';
import './widgets/app-shell/app-shell.js'; // last — see invariant above
