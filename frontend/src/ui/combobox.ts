import {closestControl,requiredElement} from './dom'
import {escape,icon} from './controls'
import type {Choice} from './components/runtime'
export function combo(id:string, label:string, source:string, multiple = false) {
  return `<div class="field"><label for="${id}">${label}</label>
    <div class="dev-combo" data-combo data-source="${source}" data-multiple="${multiple}">
      <input id="${id}" autocomplete="off" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="${id}-options" placeholder="输入关键词搜索">
      <input type="hidden" name="${id}" data-combo-result>
      <div class="combo-box" id="${id}-options" hidden>
        <div role="listbox" aria-label="${label}选项"${multiple ? ' aria-multiselectable="true"' : ''}></div>
        <p class="combo-empty" role="status" hidden>没有匹配的选项。</p>
      </div>
      <div class="combo-values" aria-label="已选内容"></div>
    </div>
  </div>`;
}

export function mountCombo(root:Element,{choices,initial,preventEscape=true,signal}:{choices:Choice[];initial?:string[];preventEscape?:boolean;signal:AbortSignal}) {
  const input = requiredElement(root,'[role="combobox"]',HTMLInputElement);
  const output = requiredElement(root,'[data-combo-result]',HTMLInputElement);
  const box = requiredElement(root,'.combo-box',HTMLElement);
  const list = requiredElement(root,'[role="listbox"]',HTMLElement);
  const empty = requiredElement(root,'.combo-empty',HTMLElement);
  const values = requiredElement(root,'.combo-values',HTMLElement);
  const multiple = root.getAttribute('data-multiple') === 'true';
  const source = choices;
  const selected = new Set(initial ?? []);
  let active = -1;
  let matches:Choice[] = [];

  function highlight() {
    const options = [...list.querySelectorAll<HTMLElement>(':scope > *')];
    options.forEach((option,index) => { option.dataset.active = String(index === active); });
    if (active < 0) input.removeAttribute('aria-activedescendant');
    else {
      input.setAttribute('aria-activedescendant',options[active].id);
      options[active].scrollIntoView({block:'nearest',inline:'nearest'});
    }
  }
  function show(query:string) {
    const needle = query.trim().toLocaleLowerCase();
    matches = source.filter(item => [item.label,item.detail,item.search].join(' ').toLocaleLowerCase().includes(needle));
    list.innerHTML = matches.map((item,index) => '<div class="combo-option" role="option" id="' + input.id + '-option-' + index +
      '" data-option="' + escape(item.id) + '" aria-selected="' + selected.has(item.id) + '"><span>' + escape(item.label) +
      (item.detail ? '<small>' + escape(item.detail) + '</small>' : '') + '</span>' + (selected.has(item.id) ? '<span aria-hidden="true">✓</span>' : '') + '</div>'
    ).join('');
    empty.hidden = Boolean(matches.length);
    box.hidden = false;
    input.setAttribute('aria-expanded','true');
    active = -1;
    highlight();
  }
  function close() {
    box.hidden = true;
    input.setAttribute('aria-expanded','false');
    input.removeAttribute('aria-activedescendant');
  }
  function sync() {
    output.value = [...selected].join(',');
    output.dispatchEvent(new Event('input',{bubbles:true}));
    values.innerHTML = source.filter(item => selected.has(item.id)).map(item =>
      '<button class="combo-value" type="button" data-remove="' + escape(item.id) + '" aria-label="移除 ' + escape(item.label) + '">' + escape(item.label) + ' ×</button>'
    ).join('');
  }
  function pick(id:string) {
    if (selected.has(id) && multiple) selected.delete(id);
    else {
      if (!multiple) selected.clear();
      selected.add(id);
    }
    input.value = '';
    sync();
    if (multiple) show(''); else close();
  }
  input.addEventListener('focus',() => show(input.value));
  input.addEventListener('click',() => { if(box.hidden) show(input.value); });
  input.addEventListener('input',() => show(input.value));
  input.addEventListener('keydown',event => {
    if (event.key === 'Escape' && !box.hidden) {
      if (preventEscape) event.preventDefault();
      close();
      return;
    }
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      if (box.hidden) show(input.value);
      if (!matches.length) return;
      if (active < 0) active = event.key === 'ArrowDown' ? 0 : matches.length-1;
      else active = (active + (event.key === 'ArrowDown' ? 1 : -1) + matches.length) % matches.length;
      highlight();
    }
    if (event.key === 'Enter' && !box.hidden && active >= 0) {
      event.preventDefault();
      pick(matches[active].id);
    }
  });
  list.addEventListener('pointerdown',event => { if(closestControl(event,'[data-option]')) event.preventDefault(); });
  list.addEventListener('click',event => {
    const option = closestControl(event,'[data-option]');
    if (option) pick(option.dataset.option ?? '');
  });
  values.addEventListener('click',event => {
    const button = closestControl(event,'[data-remove]');
    if (!button) return;
    selected.delete(button.dataset.remove ?? '');
    input.focus();
    sync();
    if (!box.hidden) show(input.value);
  });
  root.addEventListener('focusout',event => {
    if (!root.contains(event instanceof FocusEvent && event.relatedTarget instanceof Node ? event.relatedTarget : null)) close();
  });
  root.closest('form')?.addEventListener('reset',() => {
    selected.clear();
    sync();
    close();
  }, { signal });
  if (initial) sync();
}
