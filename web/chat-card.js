const headerTones = new Map([
  ['blue', 'info'], ['wathet', 'info'], ['turquoise', 'agent'], ['green', 'success'],
  ['yellow', 'warning'], ['orange', 'warning'], ['red', 'danger'],
  ['purple', 'important'], ['indigo', 'important'], ['grey', 'neutral']
]);
const actionTones = new Map([
  ['primary', 'primary'], ['primary_filled', 'primary'], ['danger', 'danger'], ['danger_filled', 'danger']
]);

function cardText(value) {
  if (typeof value === 'string') return value;
  return typeof value?.content === 'string' ? value.content : '';
}

function webURL(value) {
  if (typeof value !== 'string') return;
  try {
    const url = new URL(value);
    if (['https:', 'http:'].includes(url.protocol) && !url.username && !url.password) return url.href;
  } catch {
    // 缺少可用地址的历史操作保持只读。
  }
}

export function renderChatCard(body, { text, escape }) {
  let card;
  try {
    card = JSON.parse(body);
  } catch {
    return '';
  }
  if (!card || typeof card !== 'object') return '';

  function markdown(value) {
    return value.split(/(\*\*[^*\n]+\*\*)/u).map((part, index) =>
      index % 2 ? `<strong>${text(part.slice(2, -2))}</strong>` : text(part)).join('');
  }

  function rows(elements, depth = 0) {
    if (!Array.isArray(elements) || depth >= 24) return '';
    return elements.map(row => {
      const parts = Array.isArray(row) ? row : [row];
      const markup = parts.map(part => element(part, depth)).join('');
      if (!markup) return '';
      const actions = parts.every(part => part?.tag === 'button');
      return `<div class="${actions ? 'chat-card-actions' : 'chat-card-row'}">${markup}</div>`;
    }).join('');
  }

  function element(part, depth) {
    if (typeof part === 'string') return text(part);
    if (!part || typeof part !== 'object') return '';
    const label = cardText(part.text) || cardText(part.content) || cardText(part.alt);
    switch (part.tag) {
      case 'button': {
        if (!label.trim()) return '';
        const link = Array.isArray(part.behaviors) && part.behaviors.find(item => item?.type === 'open_url');
        const redirect = part.multi_url ?? part.multiUrl ?? link;
        const url = [part.href, part.url, redirect?.pc_url, redirect?.default_url].map(webURL).find(Boolean);
        const attrs = `class="chat-card-action" data-tone="${actionTones.get(part.type) ?? 'default'}"`;
        return url && part.disabled !== true
          ? `<a ${attrs} href="${escape(url)}" target="_blank" rel="noopener noreferrer">${text(label)}</a>`
          : `<button ${attrs} type="button" disabled>${text(label)}</button>`;
      }
      case 'a': {
        const url = webURL(part.href);
        return url && label
          ? `<a class="chat-card-link" href="${escape(url)}" target="_blank" rel="noopener noreferrer">${text(label)}</a>`
          : text(label);
      }
      case 'hr': return '<hr class="chat-card-rule">';
      case 'markdown':
      case 'lark_md': return markdown(label);
      case 'note': {
        const note = rows(part.elements, depth + 1) || text(label);
        return note ? `<div class="chat-card-note">${note}</div>` : '';
      }
      case 'action': return rows([part.actions], depth + 1);
      case 'column_set': {
        const columns = rows(part.columns, depth + 1);
        return columns ? `<div class="chat-card-columns">${columns}</div>` : '';
      }
      default: {
        const copy = part.text?.tag === 'lark_md' ? markdown(label) : text(label);
        const bold = part.style === 'bold' || (Array.isArray(part.style) && part.style.includes('bold'));
        return (bold && copy ? `<strong>${copy}</strong>` : copy) +
          rows(part.fields, depth + 1) + rows(part.elements, depth + 1);
      }
    }
  }

  const title = cardText(card.header?.title) || cardText(card.title);
  const subtitle = cardText(card.header?.subtitle);
  const content = rows(card.body?.elements ?? card.elements);
  if (!title.trim() && !subtitle.trim() && !content) return '';
  const tone = headerTones.get(card.header?.template) ?? 'info';
  const header = title || subtitle ? `<header class="chat-card-header" data-tone="${tone}">
    ${title ? `<h3>${text(title)}</h3>` : ''}${subtitle ? `<p>${text(subtitle)}</p>` : ''}</header>` : '';
  return `<article class="chat-card">${header}${content ? `<div class="chat-card-content">${content}</div>` : ''}</article>`;
}
