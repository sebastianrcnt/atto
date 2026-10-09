// TUI Markdown conventions, rendered with text nodes only. Incomplete fences and
// delimiters remain readable while streaming; no HTML parser or remote content.
import { clean, safeURL, Data } from './core';
function node(tag: string, text?: string): HTMLElement {
  const n = document.createElement(tag);
  if (text != null) n.append(document.createTextNode(text));
  return n;
}
export function inline(text: string, parent: HTMLElement, depth = 0) {
  text = clean(text);
  if (depth > 24) {
    parent.append(document.createTextNode(text));
    return;
  }
  let plain = '';
  const flush = () => {
    if (plain) parent.append(document.createTextNode(plain));
    plain = '';
  };
  for (let i = 0; i < text.length;) {
    const rest = text.slice(i);
    if (/^\\[\\`*_{}\[\]()#+.!|~<>-]/.test(rest)) {
      plain += rest[1];
      i += 2;
      continue;
    }
    if (text[i] === '\n') {
      flush();
      parent.append(node('br'));
      i++;
      continue;
    }
    const ticks = /^`+/.exec(rest);
    if (ticks) {
      const end = text.indexOf(ticks[0], i + ticks[0].length);
      if (end >= 0) {
        flush();
        let value = text.slice(i + ticks[0].length, end).replace(/\n/g, ' ');
        if (/^ .* $/.test(value) && value.trim()) value = value.slice(1, -1);
        parent.append(node('code', value));
        i = end + ticks[0].length;
        continue;
      }
    }
    const link =
      /^\[([^\]]+)\]\(([^\s]*?)\)/.exec(rest) ||
      /^<(https?:\/\/[^>]+)>/.exec(rest);
    if (link && safeURL(link[2] || link[1])) {
      flush();
      const a = node('a');
      a.setAttribute('href', link[2] || link[1]);
      a.setAttribute('rel', 'noopener noreferrer');
      a.setAttribute('target', '_blank');
      inline(link[1], a, depth + 1);
      parent.append(a);
      i += link[0].length;
      continue;
    }
    const marker = /^(\*{1,3}|_{1,3}|~~)(?=\S)/.exec(rest);
    if (
      marker &&
      !(text[i] === '_' && /[\p{L}\p{N}]/u.test(text[i - 1] || ''))
    ) {
      const delim = marker[1];
      let end = i + delim.length;
      while ((end = text.indexOf(delim, end)) >= 0) {
        if (
          end > i + delim.length &&
          !/\s/.test(text[end - 1]) &&
          !(
            delim[0] === '_' &&
            /[\p{L}\p{N}]/u.test(text[end + delim.length] || '')
          ) &&
          !(delim.length < 3 && text[end + delim.length] === delim[0])
        )
          break;
        end += delim.length;
      }
      if (end >= 0) {
        flush();
        const e = node(
          delim === '~~' ? 's' : delim.length >= 2 ? 'strong' : 'em',
        );
        if (delim.length === 3) {
          const em = node('em');
          inline(text.slice(i + delim.length, end), em, depth + 1);
          e.append(em);
        } else inline(text.slice(i + delim.length, end), e, depth + 1);
        parent.append(e);
        i = end + delim.length;
        continue;
      }
      plain += delim;
      i += delim.length;
      continue;
    }
    plain += text[i++];
  }
  flush();
}
export function splitRow(line: string) {
  line = line.trim().replace(/^\|/, '').replace(/\|$/, '');
  const cells: string[] = [];
  let cell = '',
    ticks = '';
  for (let i = 0; i < line.length;) {
    if (line[i] === '\\' && line[i + 1] === '|') {
      cell += '|';
      i += 2;
      continue;
    }
    if (line[i] === '`') {
      const run = /^`+/.exec(line.slice(i))![0];
      if (!ticks) ticks = run;
      else if (ticks === run) ticks = '';
      cell += run;
      i += run.length;
      continue;
    }
    if (line[i] === '|' && !ticks) {
      cells.push(cell.trim());
      cell = '';
      i++;
    } else cell += line[i++];
  }
  cells.push(cell.trim());
  return cells;
}
const fence = /^ {0,3}(`{3,}|~{3,})\s*([^\s`]*)/;
const heading = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const quote = /^ {0,3}> ?(.*)$/;
const list = /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/;
const rule = /^ {0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$/;
const separator = (s: string, columns: number) => {
  const cells = splitRow(s);
  return cells.length === columns && cells.every((c) => /^:?-+:?$/.test(c));
};
const starts = (s: string) =>
  fence.test(s) ||
  heading.test(s) ||
  quote.test(s) ||
  list.test(s) ||
  rule.test(s);
export function renderMarkdown(
  text: string,
  code: (s: string, p: Data) => HTMLElement,
) {
  const root = node('div');
  root.className = 'markdown';
  const lines = clean(text).replace(/\r\n?/g, '\n').split('\n');
  function blocks(lines: string[], parent: HTMLElement, depth = 0) {
    if (depth > 24) {
      parent.append(node('p', lines.join('\n')));
      return;
    }
    for (let i = 0; i < lines.length;) {
      const line = lines[i];
      let m: RegExpExecArray | null;
      if (!line.trim()) {
        i++;
        continue;
      }
      if ((m = fence.exec(line))) {
        const marker = m[1],
          language = m[2],
          body: string[] = [];
        i++;
        const close = new RegExp(
          '^ {0,3}' + marker[0] + '{' + marker.length + ',}\\s*$',
        );
        while (i < lines.length && !close.test(lines[i])) body.push(lines[i++]);
        if (i < lines.length) i++;
        parent.append(code(body.join('\n'), { language }));
        continue;
      }
      if ((m = heading.exec(line))) {
        const h = node('h' + m[1].length);
        inline(m[2], h);
        parent.append(h);
        i++;
        continue;
      }
      if (rule.test(line)) {
        parent.append(node('hr'));
        i++;
        continue;
      }
      if (quote.test(line)) {
        const inner: string[] = [];
        while (i < lines.length && (m = quote.exec(lines[i]))) {
          inner.push(m[1]);
          i++;
        }
        const q = node('blockquote');
        blocks(inner, q, depth + 1);
        parent.append(q);
        continue;
      }
      if ((m = list.exec(line))) {
        const indent = m[1].length,
          ordered = /^\d/.test(m[2]),
          group = node(ordered ? 'ol' : 'ul');
        if (ordered) group.setAttribute('start', String(parseInt(m[2], 10)));
        while (
          i < lines.length &&
          (m = list.exec(lines[i])) &&
          m[1].length === indent &&
          /^\d/.test(m[2]) === ordered
        ) {
          const li = node('li'),
            content: string[] = [m[3]],
            padding = indent + m[2].length + 1;
          i++;
          while (i < lines.length) {
            const next = list.exec(lines[i]);
            if (next && next[1].length <= indent) break;
            if (!lines[i].trim()) {
              if (
                i + 1 < lines.length &&
                /^\s/.test(lines[i + 1]) &&
                lines[i + 1].search(/\S/) > indent
              ) {
                content.push('');
                i++;
                continue;
              }
              break;
            }
            if (lines[i].search(/\S/) <= indent) break;
            content.push(
              lines[i].slice(Math.min(padding, lines[i].search(/\S/))),
            );
            i++;
          }
          const task = /^\[([ xX])\]\s+/.exec(content[0]);
          if (task) {
            li.className = 'task-item';
            const check = node('input') as HTMLInputElement;
            check.type = 'checkbox';
            check.disabled = true;
            check.checked = task[1] !== ' ';
            li.append(check);
            content[0] = content[0].slice(task[0].length);
          }
          blocks(content, li, depth + 1);
          group.append(li);
          let next = i;
          while (next < lines.length && !lines[next].trim()) next++;
          const sibling = list.exec(lines[next] || '');
          if (
            sibling &&
            sibling[1].length === indent &&
            /^\d/.test(sibling[2]) === ordered
          )
            i = next;
        }
        parent.append(group);
        continue;
      }
      if (
        i + 1 < lines.length &&
        line.includes('|') &&
        separator(lines[i + 1], splitRow(lines[i]).length)
      ) {
        const heads = splitRow(line),
          aligns = splitRow(lines[i + 1]);
        const wrap = node('div');
        wrap.className = 'table-scroll';
        const table = node('table');
        const add = (cells: string[], tag: string, container: HTMLElement) => {
          const tr = node('tr');
          heads.forEach((_, j) => {
            const c = node(tag);
            inline(cells[j] || '', c);
            c.style.textAlign = /^:.*:$/.test(aligns[j])
              ? 'center'
              : /:$/.test(aligns[j])
                ? 'right'
                : 'left';
            tr.append(c);
          });
          container.append(tr);
        };
        if (heads.some(Boolean)) {
          const thead = node('thead');
          add(heads, 'th', thead);
          table.append(thead);
        }
        const tbody = node('tbody');
        i += 2;
        while (
          i < lines.length &&
          lines[i].trim() &&
          lines[i].includes('|') &&
          !starts(lines[i])
        )
          add(splitRow(lines[i++]), 'td', tbody);
        table.append(tbody);
        wrap.append(table);
        parent.append(wrap);
        continue;
      }
      const para: string[] = [];
      while (
        i < lines.length &&
        lines[i].trim() &&
        (!para.length || !starts(lines[i]))
      ) {
        if (
          para.length &&
          i + 1 < lines.length &&
          lines[i].includes('|') &&
          separator(lines[i + 1], splitRow(lines[i]).length)
        )
          break;
        const s = lines[i++];
        para.push(
          /(?: {2,}|\\)$/.test(s)
            ? s.replace(/(?: {2,}|\\)$/, '') + '\n'
            : s.trim() + ' ',
        );
      }
      const p = node('p');
      inline(para.join('').trimEnd(), p);
      parent.append(p);
    }
  }
  blocks(lines, root);
  return root;
}
