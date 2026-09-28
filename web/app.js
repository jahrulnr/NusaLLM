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
// Rendering primitives — code-block and mermaid-diagram before
// markdown-view (which instantiates both), all before message-thread.
import './widgets/code-block/code-block.js';
import './widgets/mermaid-diagram/mermaid-diagram.js';
import './widgets/thinking-block/thinking-block.js';
import './widgets/tool-activity/tool-activity.js';
import './widgets/markdown-view/markdown-view.js';
import './widgets/message-thread/message-thread.js';
import './widgets/chat-composer/chat-composer.js';
import './widgets/model-picker/model-picker.js';
import './widgets/workspace-picker/workspace-picker.js';
import './widgets/provider-manager/provider-manager.js';
import './widgets/settings-editor/settings-editor.js';
import './widgets/tool-catalog/tool-catalog.js';
import './widgets/settings-page/settings-page.js';
import './widgets/toast-stack/toast-stack.js';
import './widgets/app-shell/app-shell.js'; // last — see invariant above
