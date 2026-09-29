// Pure syntax tokenizer — no DOM. tokenizeCode returns
// [{type,text}] where concatenating `text` reproduces the source
// byte-for-code-unit; unknown languages return a single plain token.

const JSLIKE = ['js', 'javascript', 'ts', 'typescript', 'jsx', 'tsx', 'mjs', 'cjs', 'java', 'c', 'cpp', 'csharp', 'cs', 'rust', 'rs'];
const SHELL = ['sh', 'bash', 'zsh', 'shell', 'console'];

const KW = {
  clike: 'break|case|catch|class|const|continue|default|delete|do|else|enum|export|extends|finally|for|function|if|implements|import|in|instanceof|interface|let|new|of|private|protected|public|return|static|super|switch|this|throw|try|type|typeof|var|void|while|with|yield|async|await|null|true|false|undefined|struct|fn|pub|impl|use|mod|match|mut|ref|self|where',
  go: 'break|case|chan|const|continue|default|defer|else|fallthrough|for|func|go|goto|if|import|interface|map|package|range|return|select|struct|switch|type|var|nil|true|false|iota',
  py: 'and|as|assert|async|await|break|class|continue|def|del|elif|else|except|finally|for|from|global|if|import|in|is|lambda|nonlocal|not|or|pass|raise|return|try|while|with|yield|None|True|False|self',
  sh: 'case|do|done|elif|else|esac|fi|for|function|if|in|local|return|select|then|until|while|echo|export|set|unset|cd|exit|source|alias|eval|exec|read|shift|trap|true|false',
  json: 'true|false|null',
};

// Sticky + multiline patterns — `^` must assert at line starts mid-string.
function rules(list) {
  return list.map(([src, type]) => [new RegExp(src, 'ym'), type]);
}

const STR_DQ = '"[^"\\\\\\n]*(?:\\\\.[^"\\\\\\n]*)*"';
const STR_SQ = "'[^'\\\\\\n]*(?:\\\\.[^'\\\\\\n]*)*'";
const STR_TQ = '`[^`]*`';

const LANGS = {
  clike: rules([
    ['\\/\\/[^\\n]*', 'comment'],
    ['\\/\\*[\\s\\S]*?\\*\\/', 'comment'],
    [STR_DQ, 'string'], [STR_SQ, 'string'], [STR_TQ, 'string'],
    ['\\b(' + KW.clike + ')\\b', 'keyword'],
    ['\\b\\d[\\d_]*(?:\\.[\\d_]+)?(?:[eE][+-]?\\d+)?\\b|0x[\\da-fA-F_]+', 'number'],
    ['[A-Za-z_$][\\w$]*(?=\\s*\\()', 'operator'],
  ]),
  go: rules([
    ['\\/\\/[^\\n]*', 'comment'],
    ['\\/\\*[\\s\\S]*?\\*\\/', 'comment'],
    [STR_DQ, 'string'], [STR_TQ, 'string'], ["'(?:\\\\.|[^'\\\\])'", 'string'],
    ['\\b(' + KW.go + ')\\b', 'keyword'],
    ['\\b\\d[\\d_]*(?:\\.[\\d_]+)?(?:[eE][+-]?\\d+)?\\b|0x[\\da-fA-F_]+', 'number'],
    ['\\b[A-Z_a-z][\\w]*(?=\\s*\\()', 'operator'],
  ]),
  py: rules([
    ['#[^\\n]*', 'comment'],
    ['"""[\\s\\S]*?"""|\'\'\'[\\s\\S]*?\'\'\'', 'string'],
    [STR_DQ, 'string'], [STR_SQ, 'string'],
    ['\\b(' + KW.py + ')\\b', 'keyword'],
    ['\\b\\d[\\d_]*(?:\\.[\\d_]+)?\\b', 'number'],
    ['@[\\w.]+', 'operator'],
  ]),
  sh: rules([
    ['#[^\\n]*', 'comment'],
    [STR_DQ, 'string'], [STR_SQ, 'string'],
    ['\\$\\{[^}]*\\}|\\$[\\w@#?*!]+', 'operator'],
    ['\\b(' + KW.sh + ')\\b', 'keyword'],
    ['\\b\\d+\\b', 'number'],
  ]),
  json: rules([
    [STR_DQ + '(?=\\s*:)', 'keyword'],
    [STR_DQ, 'string'],
    ['-?\\b\\d+(?:\\.\\d+)?(?:[eE][+-]?\\d+)?\\b', 'number'],
    ['\\b(' + KW.json + ')\\b', 'keyword'],
    ['[{}\\[\\]:,]', 'operator'],
  ]),
  css: rules([
    ['\\/\\*[\\s\\S]*?\\*\\/', 'comment'],
    [STR_DQ, 'string'], [STR_SQ, 'string'],
    ['@[\\w-]+', 'keyword'],
    ['#[\\da-fA-F]{3,8}\\b', 'number'],
    ['\\b-?[a-zA-Z][\\w-]*(?=\\s*:)', 'keyword'],
    ['\\b\\d+(?:\\.\\d+)?(?:px|em|rem|%|vh|vw|ms|s|fr|deg)?\\b', 'number'],
  ]),
  html: rules([
    ['<!--[\\s\\S]*?-->', 'comment'],
    ['<\\/?[A-Za-z][\\w-]*|\\/?>', 'keyword'],
    ['[A-Za-z-]+(?=\\s*=)', 'operator'],
    [STR_DQ, 'string'], [STR_SQ, 'string'],
  ]),
  md: rules([
    ['^\\s*#{1,6}\\s[^\\n]*', 'keyword'],
    ['^\\s*(```+|~~~+)[^\\n]*', 'string'],
    ['\\*\\*[^*]+\\*\\*|__[^_]+__', 'operator'],
    ['`[^`\\n]*`', 'string'],
    ['\\[[^\\]\\n]*\\]\\([^)\\n]*\\)', 'operator'],
  ]),
  diff: rules([
    // Headers must match before +/- content — `+++`/`---` would
    // otherwise classify as insert/delete lines.
    ['^@@[^\\n]*|^diff [^\\n]*|^index [^\\n]*|^--- [^\\n]*|^\\+\\+\\+ [^\\n]*', 'keyword'],
    ['^\\+[^\\n]*', 'insert'],
    ['^-[^\\n]*', 'delete'],
    ['^![^\\n]*', 'comment'],
  ]),
};

const ALIAS = {};
for (const l of JSLIKE) ALIAS[l] = 'clike';
for (const l of SHELL) ALIAS[l] = 'sh';
Object.assign(ALIAS, {
  go: 'go', golang: 'go',
  py: 'py', python: 'py', python3: 'py',
  json: 'json', jsonc: 'json',
  css: 'css', scss: 'css',
  html: 'html', xml: 'html', svg: 'html', vue: 'html',
  md: 'md', markdown: 'md',
  diff: 'diff', patch: 'diff',
});

export function tokenizeCode(source, language) {
  const src = String(source ?? '');
  const lang = ALIAS[String(language || '').toLowerCase()];
  const langRules = lang && LANGS[lang];
  if (!langRules) return src ? [{ type: 'plain', text: src }] : [];

  const out = [];
  let i = 0;
  const push = (type, text) => {
    if (!text) return;
    const last = out[out.length - 1];
    if (last && last.type === type) last.text += text;
    else out.push({ type, text });
  };
  while (i < src.length) {
    let matched = false;
    for (const [re, type] of langRules) {
      re.lastIndex = i;
      const m = re.exec(src);
      if (m && m[0].length) {
        push(type, m[0]);
        i += m[0].length;
        matched = true;
        break;
      }
    }
    if (!matched) { push('plain', src[i]); i++; }
  }
  return out;
}
