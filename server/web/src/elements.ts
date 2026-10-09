// Beautiful UI Code Block, Diff Table, Task Rows and Approval Card adapted to
// DOM/catalog data (MIT, Shane Levine 2026; THIRD_PARTY_NOTICES).
import { Tree, Data, clean, safeURL, catalog } from './core';
export function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  text?: any,
  cls?: string,
): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text != null) e.append(document.createTextNode(clean(text)));
  if (cls) e.className = cls;
  return e;
}
export function button(text: string, fn: () => void, disabled = false) {
  const b = el('button', text);
  b.type = 'button';
  b.disabled = disabled;
  b.onclick = fn;
  return b;
}
export function inline(text: string, parent: HTMLElement) {
  const re = /(`[^`]+`|\*\*[^*]+\*\*|\*[^*]+\*|\[[^\]]+\]\([^\s)]+\))/g;
  let last = 0;
  for (const m of text.matchAll(re)) {
    parent.append(document.createTextNode(clean(text.slice(last, m.index))));
    const s = m[0];
    if (s.startsWith('[')) {
      const a = /^\[([^\]]+)\]\((.*)\)$/.exec(s)!;
      if (safeURL(a[2])) {
        const e = el('a', a[1]);
        e.href = a[2];
        e.rel = 'noopener noreferrer';
        e.target = '_blank';
        parent.append(e);
      } else parent.append(document.createTextNode(clean(s)));
    } else {
      const tag = s[0] === '`' ? 'code' : s.startsWith('**') ? 'strong' : 'em';
      const n = tag === 'strong' ? 2 : 1;
      parent.append(el(tag, s.slice(n, -n)));
    }
    last = m.index! + s.length;
  }
  parent.append(document.createTextNode(clean(text.slice(last))));
}
export function markdown(text: string) {
  const root = el('div', null, 'markdown');
  const lines = clean(text).split('\n');
  let fence: string[] | null = null;
  let language = '';
  let list: HTMLElement | null = null;
  for (const line of lines) {
    if (/^\s*```/.test(line)) {
      if (fence) {
        root.append(code(fence.join('\n'), { language }));
        fence = null;
      } else {
        fence = [];
        language = line.replace(/^\s*```/, '');
      }
      list = null;
      continue;
    }
    if (fence) {
      fence.push(line);
      continue;
    }
    const h = /^(#{1,6})\s+(.*)$/.exec(line);
    const li = /^\s*(?:[-*+] |\d+\. )(.*)$/.exec(line);
    const quote = /^>\s?(.*)$/.exec(line);
    if (li) {
      if (!list) {
        list = el(/^\s*\d/.test(line) ? 'ol' : 'ul');
        root.append(list);
      }
      const e = el('li');
      inline(li[1], e);
      list.append(e);
      continue;
    }
    list = null;
    const e = h ? document.createElement('h' + h[1].length) : quote ? el('blockquote') : el('p');
    inline(h ? h[2] : quote ? quote[1] : line, e);
    root.append(e);
  }
  if (fence) root.append(code(fence.join('\n'), { language }));
  return root;
}
export function code(source: string, p: Data = {}, diff = false) {
  const root = el('div', null, 'code-block');
  if (p.path || p.language) root.append(el('div', p.path || p.language, 'code-title'));
  const pre = el('pre');
  pre.className = p.wrap === 'wrap' ? 'code-wrap' : 'code-truncate';
  const body = el('code');
  clean(source)
    .split('\n')
    .forEach((s, i) => {
      const line = el('span', null, 'code-line');
      if (diff) {
        line.classList.add(
          /^(diff |index |---|\+\+\+)/.test(s)
            ? 'muted'
            : s.startsWith('@@')
              ? 'diffHunk'
              : s.startsWith('+')
                ? 'diffAdd'
                : s.startsWith('-')
                  ? 'diffRemove'
                  : 'text',
        );
      }
      if (p.lineNumbers)
        line.append(el('span', String(i + (p.startLine || 1)).padStart(4) + ' │ ', 'gutter'));
      line.append(document.createTextNode(s));
      body.append(line);
    });
  pre.append(body);
  root.append(pre);
  return root;
}
export class Local {
  values = new Map<string, string>();
  drafts = new Map<string, string>();
  open = new Map<string, boolean>();
  sync(tree: Tree | null) {
    const keys = new Set<string>();
    const walk = (n: Tree) => {
      if (!catalog.includes(n.type)) return;
      if (n.key) {
        keys.add(n.key);
        if (n.type === 'Input' || n.type === 'Select') {
          const v =
            n.props.value ??
            (n.type === 'Select' ? n.props.options?.find((o: Data) => !o.disabled)?.value : '') ??
            '';
          if (this.values.get(n.key) !== v) {
            this.values.set(n.key, v);
            this.drafts.set(n.key, v);
          }
        }
        if (n.type === 'Collapse' && !this.open.has(n.key))
          this.open.set(n.key, !!n.props.defaultOpen);
      }
      n.children?.forEach(walk);
    };
    if (tree) walk(tree);
    for (const m of [this.values, this.drafts, this.open])
      for (const k of m.keys()) if (!keys.has(k)) m.delete(k);
  }
}
export type Context = {
  site: string;
  id: string;
  rev: number;
  enabled: boolean;
  local: Local;
  action: (key: string, type: string, value?: string) => void;
  engine?: (n: Tree) => HTMLElement;
  image: (resource: string, img: HTMLImageElement) => void;
  focus?: boolean;
};
function fallback(n: Tree): string {
  const p = n.props || {};
  return (
    [p.text || p.source || p.label || p.alt || '', ...(n.children || []).map(fallback)]
      .filter(Boolean)
      .join('\n') || '[unsupported: ' + n.type + ']'
  );
}
// Client-side catalog validation mirrors ui.ValidateDisplay before layout. The
// worker is authoritative; this also bounds work after replay or version skew.
const themes = [
  'text',
  'muted',
  'accent',
  'success',
  'warning',
  'error',
  'border',
  'surface',
  'diffAdd',
  'diffRemove',
  'diffHunk',
];
const schemas: Record<string, string> = {
  Box: 'flexDirection:s:column,row gap:i:0,16 padding:i:0,16 width:w height:i:1,256 grow:n:0,1000000 align:s:start,center,end borderStyle:s:none,single,round,double,ascii',
  Text: 'text:s bold:b italic:b underline:b wrap:s:wrap,truncate maxLines:i:1,256',
  Markdown: 'text!:s maxLines:i:1,256',
  Code: 'source!:s language:s path:s startLine:i:1,9007199254740991 lineNumbers:b wrap:s:wrap,truncate',
  Diff: 'source!:s path:s lineNumbers:b wrap:s:wrap,truncate',
  Link: 'href!:s label:s',
  Button: 'label!:s plain:b hotkey:s disabled:b autoFocus:b',
  Input: 'label:s value:s placeholder:s submitLabel:s maxLength:i:0,131072 disabled:b autoFocus:b',
  Select: 'options!:a label:s value:s disabled:b autoFocus:b',
  List: 'mode:s:list,table rows!:a columns:a emptyText:s',
  Progress: 'value:n:0,1 label:s width:i:1,512',
  Collapse: 'title!:s defaultOpen:b previewLines:i:0,256',
  Image: 'resource!:s alt!:s columns:i:1,512 rows:i:1,256 fit:s:contain,cover',
};
function props(p: Data, schema: string, common = true) {
  const rules = new Map<string, string>();
  if (common) {
    rules.set('color', 's');
    rules.set('backgroundColor', 's');
  }
  for (const field of schema.split(' ')) {
    const [key, type, ...range] = field.split(':');
    const name = key.replace('!', '');
    rules.set(name, [type, ...range].join(':'));
    if (key.endsWith('!') && !(name in p)) throw Error('missing prop');
  }
  for (const [key, v] of Object.entries(p)) {
    const rule = rules.get(key);
    if (!rule) throw Error('unknown prop');
    const [type, range] = rule.split(':');
    if (type === 's' && (typeof v !== 'string' || (range && !range.split(',').includes(v))))
      throw Error('string prop');
    if ((type === 'b' && typeof v !== 'boolean') || (type === 'a' && !Array.isArray(v)))
      throw Error('prop type');
    if (type === 'w' && v !== 'fill' && (!Number.isInteger(v) || v < 1 || v > 512))
      throw Error('width');
    if (type === 'n' || type === 'i') {
      if (typeof v !== 'number' || !Number.isFinite(v) || (type === 'i' && !Number.isInteger(v)))
        throw Error('number prop');
      if (range) {
        const [lo, hi] = range.split(',').map(Number);
        if (v < lo || v > hi) throw Error('prop range');
      }
    }
    if ((key === 'color' || key === 'backgroundColor') && !themes.includes(v)) throw Error('theme');
  }
}
function utf8Length(s: string) {
  let n = 0;
  for (const c of s) {
    const p = c.codePointAt(0)!;
    n += p <= 127 ? 1 : p <= 2047 ? 2 : p <= 65535 ? 3 : 4;
  }
  return n;
}
function propText(v: any, depth = 0): number {
  if (depth > 32) throw Error('props depth');
  if (typeof v === 'string') return utf8Length(v);
  if (Array.isArray(v)) return v.reduce((n, x) => n + propText(x, depth + 1), 0);
  if (v && typeof v === 'object')
    return Object.values(v).reduce<number>((n, x) => n + propText(x, depth + 1), 0);
  return 0;
}
export function validTree(tree: Tree, site: string, id: string) {
  let count = 0,
    refs = 0;
  const keys = new Set<string>(),
    hotkeys = new Set<string>();
  let text = 0;
  function walk(n: Tree, depth: number) {
    if (!n || typeof n.type !== 'string' || !n.props || ++count > 2048 || depth > 32)
      throw Error('tree limit');
    if (n.key) {
      if (keys.has(n.key) || n.key.length > 128 || n.key === '$site') throw Error('key');
      keys.add(n.key);
    }
    text += propText(n.props);
    if (text > 131072) throw Error('text limit');
    const p = n.props;
    if (n.type === 'engine') {
      const fields =
        site === 'toolCall'
          ? ['description', 'output']
          : site === 'notice'
            ? ['text', 'title']
            : ['text'];
      if (
        ++refs > 1 ||
        !['userMessage', 'assistantMessage', 'toolCall', 'notice'].includes(site) ||
        p.site !== site ||
        p.id !== id ||
        Object.keys(p).length !== 3 ||
        !p.overrides ||
        Object.entries(p.overrides).some(
          ([k, v]) => !fields.includes(k) || typeof v !== 'string',
        ) ||
        n.children?.length ||
        n.events?.length
      )
        throw Error('engine reference');
      return;
    }
    const schema = schemas[n.type];
    if (schema) {
      props(p, schema);
      const required = ({ Button: 'press', Input: 'submit', Select: 'select' } as Data)[n.type];
      const allowed = n.type === 'Input' ? ['submit', 'input'] : required ? [required] : [];
      if (
        new Set(n.events || []).size !== (n.events || []).length ||
        (n.events || []).some((e) => !allowed.includes(e)) ||
        (required && !n.events?.includes(required))
      )
        throw Error('events');
      if (['Button', 'Input', 'Select', 'Collapse'].includes(n.type) && !n.key)
        throw Error('control key');
      if (
        (site === 'status' || site === 'toast') &&
        ['Button', 'Input', 'Select', 'Collapse'].includes(n.type)
      )
        throw Error('passive site');
      if (p.hotkey) {
        if (!/^[a-z0-9]$/.test(p.hotkey) || hotkeys.has(p.hotkey)) throw Error('hotkey');
        hotkeys.add(p.hotkey);
      }
      if (n.type === 'Link' && !safeURL(p.href)) throw Error('link');
      if (n.type === 'Markdown')
        for (const match of p.text.matchAll(/\]\(([^)]*)\)|<(https?:\/\/[^>]+)>/g))
          if (!safeURL(match[1] || match[2])) throw Error('markdown link');
      if (n.type === 'Image' && !/^[A-Za-z0-9_-]{1,128}$/.test(p.resource))
        throw Error('image resource');
      if (n.type === 'Select') {
        const seen = new Set<string>();
        let enabled = 0;
        for (const o of p.options) {
          props(o, 'value!:s label!:s description:s disabled:b', false);
          if (seen.has(o.value)) throw Error('option');
          seen.add(o.value);
          if (!o.disabled) enabled++;
        }
        if (!enabled || (p.value != null && !seen.has(p.value))) throw Error('select');
      }
      if (n.type === 'List') {
        const cols = p.columns || [];
        for (const c of cols) props(c, 'label!:s width:i:1,512 align:s:start,end', false);
        const size = p.mode === 'table' ? cols.length : 1;
        if (!size) throw Error('table');
        const rows = new Set<string>();
        for (const r of p.rows) {
          props(r, 'key!:s cells!:a', false);
          if (
            !r.key ||
            r.key.length > 128 ||
            rows.has(r.key) ||
            r.cells.length !== size ||
            r.cells.some((v: any) => typeof v !== 'string')
          )
            throw Error('row');
          rows.add(r.key);
        }
      }
      if (!['Box', 'Collapse', 'Text'].includes(n.type) && n.children?.length)
        throw Error('leaf children');
      if (n.type === 'Text' && n.children?.some((c) => c.type !== 'Text'))
        throw Error('text spans');
    }
    for (const c of n.children || []) walk(c, depth + 1);
  }
  try {
    walk(tree, 1);
    if (utf8Length(JSON.stringify(tree)) > 262144) return false;
    return true;
  } catch {
    return false;
  }
}
export function render(tree: Tree | null, c: Context): HTMLElement {
  if (!tree) {
    c.local.sync(null);
    return el('span');
  }
  if (!validTree(tree, c.site, c.id)) return el('div', 'Drawing unavailable', 'muted');
  c.local.sync(tree);
  const hotkeys = new Map<string, HTMLButtonElement>();
  const draw = (n: Tree): HTMLElement => {
    const p = n.props || {};
    let e: HTMLElement;
    const send = (type: string, v?: string) => {
      if (c.enabled && !p.disabled && n.events?.includes(type)) c.action(n.key!, type, v);
    };
    const disabled = !c.enabled || !!p.disabled;
    switch (n.type) {
      case 'engine':
        return c.engine ? c.engine(n) : el('span', '[original item unavailable]');
      case 'Box': {
        e = el('div', null, 'ui-box');
        Object.assign(e.style, {
          flexDirection: p.flexDirection || 'column',
          gap: `${p.gap || 0}${p.flexDirection === 'row' ? 'ch' : 'lh'}`,
          padding: `${p.padding || 0}lh ${p.padding || 0}ch`,
          alignItems: { start: 'stretch', center: 'center', end: 'flex-end' }[p.align || 'start'],
          width: typeof p.width === 'number' ? p.width + 'ch' : '100%',
          flexGrow: String(p.grow || 0),
          height: p.height ? p.height + 'lh' : 'auto',
          flexShrink: typeof p.width === 'number' ? '0' : '1',
        });
        if (p.borderStyle && p.borderStyle !== 'none') {
          e.classList.add('ui-border');
          e.style.borderStyle = p.borderStyle === 'double' ? 'double' : 'solid';
          e.style.borderRadius = p.borderStyle === 'round' ? '0.6ch' : '0';
        }
        for (const child of n.children || []) {
          const ch = draw(child);
          if (p.flexDirection === 'row' && typeof child.props.width !== 'number') {
            ch.style.flex = `${child.props.grow || 1} 1 0`;
            ch.style.width = 'auto';
          }
          e.append(ch);
        }
        break;
      }
      case 'Text':
        e = el('span', p.text || '', 'ui-text');
        if (p.bold) e.style.fontWeight = '700';
        if (p.italic) e.style.fontStyle = 'italic';
        if (p.underline) e.style.textDecoration = 'underline';
        for (const ch of n.children || []) e.append(draw(ch));
        break;
      case 'Markdown':
        e = markdown(p.text);
        break;
      case 'Code':
      case 'Diff':
        e = code(p.source, p, n.type === 'Diff');
        break;
      case 'Link': {
        const a = el('a', p.label || p.href);
        a.href = p.href;
        a.rel = 'noopener noreferrer';
        a.target = '_blank';
        e = a;
        break;
      }
      case 'Button': {
        const b = button(
          (p.hotkey ? p.hotkey + ': ' : '') + p.label,
          () => send('press'),
          disabled,
        );
        b.dataset.key = n.key;
        if (p.plain) b.classList.add('plain');
        if (p.hotkey) hotkeys.set(p.hotkey, b);
        e = b;
        break;
      }
      case 'Input': {
        const form = el('form');
        const label = el('label', p.label);
        const input = el('input');
        input.type = 'text';
        input.dataset.key = n.key;
        input.value = c.local.drafts.get(n.key!) || '';
        input.placeholder = p.placeholder || '';
        input.maxLength = p.maxLength ?? 4096;
        input.disabled = disabled;
        input.setAttribute('aria-label', p.label || p.placeholder || 'Input');
        let timer: ReturnType<typeof setTimeout>;
        input.oninput = () => {
          c.local.drafts.set(n.key!, input.value);
          clearTimeout(timer);
          const v = input.value;
          timer = setTimeout(() => {
            if (input.isConnected) send('input', v);
          }, 100);
        };
        form.onsubmit = (ev) => {
          ev.preventDefault();
          clearTimeout(timer);
          send('submit', input.value);
        };
        label.append(input);
        form.append(
          label,
          button(p.submitLabel || 'submit', () => send('submit', input.value), disabled),
        );
        e = form;
        break;
      }
      case 'Select': {
        const label = el('label', p.label || '');
        const select = el('select');
        select.dataset.key = n.key;
        select.disabled = disabled;
        select.setAttribute('aria-label', p.label || 'Select');
        for (const o of p.options || []) {
          const opt = el('option', o.label + (o.description ? ' — ' + o.description : ''));
          opt.value = o.value;
          opt.disabled = !!o.disabled;
          select.append(opt);
        }
        select.value = c.local.drafts.get(n.key!) || '';
        select.onkeydown = (ev) => {
          if (ev.key === 'ArrowUp' || ev.key === 'ArrowDown') {
            ev.preventDefault();
            const options = Array.from(select.options),
              direction = ev.key === 'ArrowUp' ? -1 : 1;
            let i = select.selectedIndex;
            for (let count = 0; count < options.length; count++) {
              i = (i + direction + options.length) % options.length;
              if (!options[i].disabled) break;
            }
            select.selectedIndex = i;
            c.local.drafts.set(n.key!, select.value);
          } else if (ev.key === 'Enter') {
            ev.preventDefault();
            send('select', select.value);
          }
        };
        select.onchange = () => {
          c.local.drafts.set(n.key!, select.value);
          send('select', select.value);
        };
        label.append(select);
        e = label;
        break;
      }
      case 'List': {
        if (!p.rows?.length) {
          e = el('div', p.emptyText || 'No items');
          break;
        }
        if (p.mode !== 'table') {
          const list = el('ul');
          for (const r of p.rows) list.append(el('li', r.cells[0]));
          e = list;
          break;
        }
        const table = el('table');
        const head = el('thead');
        const tr = el('tr');
        for (const col of p.columns || []) tr.append(el('th', col.label));
        head.append(tr);
        table.append(head);
        const body = el('tbody');
        for (const r of p.rows) {
          const row = el('tr');
          r.cells.forEach((cell: string, i: number) => {
            const td = el('td', cell);
            const col = p.columns[i];
            if (col.width) td.style.maxWidth = col.width + 'ch';
            td.style.textAlign = col.align === 'end' ? 'right' : 'left';
            row.append(td);
          });
          body.append(row);
        }
        table.append(body);
        e = table;
        break;
      }
      case 'Progress': {
        const label = el('label', p.label || '');
        const progress = el('progress');
        progress.max = 1;
        if (p.value != null) progress.value = p.value;
        progress.setAttribute('aria-label', p.label || 'Progress');
        progress.style.width = (p.width || 20) + 'ch';
        if (p.value != null)
          label.append(document.createTextNode(' ' + Math.round(p.value * 100) + '% '));
        label.append(progress);
        e = label;
        break;
      }
      case 'Collapse': {
        const details = el('details');
        details.dataset.key = n.key;
        details.open = !!c.local.open.get(n.key!);
        details.append(el('summary', p.title));
        for (const ch of n.children || []) details.append(draw(ch));
        if (p.previewLines) {
          const preview = el(
            'div',
            (n.children || []).map(fallback).join('\n'),
            'collapse-preview',
          );
          preview.style.maxHeight = p.previewLines + 'lh';
          details.append(preview);
        }
        details.ontoggle = () => c.local.open.set(n.key!, details.open);
        e = details;
        break;
      }
      case 'Image': {
        const img = el('img');
        img.alt = p.alt;
        img.style.width = (p.columns || 40) + 'ch';
        img.style.maxHeight = (p.rows || 12) + 'lh';
        img.style.objectFit = p.fit || 'contain';
        c.image(p.resource, img);
        e = img;
        break;
      }
      default:
        return el('div', fallback(n), 'ui-text');
    }
    if (n.key) e.dataset.key = n.key;
    for (const k of ['color', 'backgroundColor']) {
      if (
        [
          'text',
          'muted',
          'accent',
          'success',
          'warning',
          'error',
          'border',
          'surface',
          'diffAdd',
          'diffRemove',
          'diffHunk',
        ].includes(p[k])
      )
        e.style[k === 'color' ? 'color' : 'backgroundColor'] = `var(--${p[k]})`;
    }
    if (p.wrap === 'truncate') e.classList.add('truncate');
    if (p.maxLines) {
      e.style.maxHeight = p.maxLines + 'lh';
      e.style.overflow = 'hidden';
    }
    if (p.autoFocus && c.focus && !disabled) e.dataset.autofocus = 'true';
    return e;
  };
  const root = draw(tree);
  root.classList.add('ui-tree');
  root.onkeydown = (ev) => {
    if (
      ev.target instanceof HTMLInputElement ||
      ev.target instanceof HTMLSelectElement ||
      ev.ctrlKey ||
      ev.metaKey ||
      ev.altKey
    )
      return;
    const b = hotkeys.get(ev.key);
    if (b && !b.disabled) {
      ev.preventDefault();
      b.click();
    }
  };
  return root;
}
