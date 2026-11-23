import { object } from './chat-contracts'
const headerTones = new Map([
  ['blue', 'info'], ['wathet', 'info'], ['turquoise', 'agent'], ['green', 'success'],
  ['yellow', 'warning'], ['orange', 'warning'], ['red', 'danger'],
  ['purple', 'important'], ['indigo', 'important'], ['grey', 'neutral']
]);
const actionTones = new Map([
  ['primary', 'primary'], ['primary_filled', 'primary'], ['danger', 'danger'], ['danger_filled', 'danger']
]);

function cardText(value:unknown) {
  if (typeof value === 'string') return value;
  return object(value) && typeof value.content === 'string' ? value.content : '';
}

function webURL(value:unknown) {
  if (typeof value !== 'string') return;
  try {
    const url = new URL(value);
    if (['https:', 'http:'].includes(url.protocol) && !url.username && !url.password) return url.href;
  } catch {
    // 缺少可用地址的历史操作保持只读。
  }
}

export function renderChatCard(body:string|undefined, { text, escape, image, openChat }:{text:(s:string)=>string; escape:(s:string)=>string; image?:(key:string)=>string; openChat?:string}) {
  if (!body) return '';
  let card:unknown;
  try {
    card = JSON.parse(body);
  } catch {
    return '';
  }
  if (!object(card)) return '';

  // Original cards may carry localized body content.
  const localized = (value:unknown) => object(value) ? value.zh_cn ?? value.en_us ?? Object.values(value)[0] : undefined;
  function markdown(value:string) {
    return value.split(/(\*\*[^*\n]+\*\*)/u).map((part, index) =>
      index % 2 ? `<strong>${text(part.slice(2, -2))}</strong>` : text(part)).join('');
  }

  function rows(elements:unknown, depth = 0):string {
    if (!Array.isArray(elements) || depth >= 24) return '';
    return elements.map(row => {
      const parts = Array.isArray(row) ? row : [row];
      const markup = parts.map(part => element(part, depth)).join('');
      if (!markup) return '';
      const actions = parts.every(part => part?.tag === 'button');
      return `<div class="${actions ? 'chat-card-actions' : 'chat-card-row'}">${markup}</div>`;
    }).join('');
  }

  function element(part:unknown, depth:number):string {
    if (typeof part === 'string') return text(part);
    if (!object(part)) return '';
    const label = cardText(part.text) || cardText(part.content) || cardText(localized(part.i18n_content)) || cardText(part.alt);
    switch (part.tag) {
      case 'img':
      case 'image': return image?.(cardText(part.img_key || part.image_key)) || text(label);
      case 'collapsible_panel': return rows([part.header, ...(Array.isArray(part.elements) ? part.elements : [])], depth + 1);
      case 'button': {
        if (!label.trim()) return '';
        const link = Array.isArray(part.behaviors) && part.behaviors.find(item => item?.type === 'open_url');
        const target = part.multi_url ?? link;
        const redirect = object(target) ? target : {};
        const url = [part.href, part.url, redirect?.pc_url, redirect?.default_url].map(webURL).find(Boolean);
        const attrs = `class="chat-card-action" data-tone="${actionTones.get(cardText(part.type)) ?? 'default'}"`;
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
        const copy = object(part.text) && part.text.tag === 'lark_md' ? markdown(label) : text(label);
        const bold = part.style === 'bold' || (Array.isArray(part.style) && part.style.includes('bold'));
        return (bold && copy ? `<strong>${copy}</strong>` : copy) +
          rows(part.fields, depth + 1) + rows(part.elements, depth + 1);
      }
    }
  }

  const heading = object(card.header) ? card.header : {};
  const title = cardText(heading.title) || cardText(card.title);
  const subtitle = cardText(heading.subtitle);
  let content = rows((object(card.body) ? card.body.elements : undefined) ?? localized(card.i18n_elements) ?? card.elements);
  // Older IM history can consist entirely of the platform's upgrade illustration and caption.
  const summaryParts:unknown[] = Array.isArray(card.elements) ? card.elements.flat() : [];
  const upgrade = (part:unknown) => object(part) && /^请升级至最新版本客户端[，,]\s*以查看内容[。.]?$/.test(cardText(part?.text) || cardText(part?.content));
  if (summaryParts.some(upgrade) && summaryParts.every(part => upgrade(part) || object(part) && (['img','image'].includes(cardText(part.tag)) || (part.tag === 'text' && !cardText(part.text).trim())))) {
    content = '<p>飞书仅返回了这张卡片的摘要。</p>' + (openChat ? `<a class="chat-card-link" href="${escape(openChat)}" target="_blank" rel="noopener noreferrer">在飞书中打开会话</a>` : '');
  }
  if (!title.trim() && !subtitle.trim() && !content) return '';
  const tone = headerTones.get(cardText(heading.template)) ?? 'info';
  const header = title || subtitle ? `<header class="chat-card-header" data-tone="${tone}">
    ${title ? `<h3>${text(title)}</h3>` : ''}${subtitle ? `<p>${text(subtitle)}</p>` : ''}</header>` : '';
  return `<article class="chat-card">${header}${content ? `<div class="chat-card-content">${content}</div>` : ''}</article>`;
}
