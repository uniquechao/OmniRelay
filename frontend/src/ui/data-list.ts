import {ApiError} from '../api/session-client'
import {mount as mountLoading} from './loading'
import {closestControl,requiredElement} from './dom'
import {escape,icon,toast} from './controls'
import type {PageState,RemotePage} from './components/Widgets'
type Pair={left:number;right:number}
type ListColumn<Row>={key:string;label:string;secondary?:boolean;sort?:true | ((row:Row)=>string);render:(row:Row)=>string}
export type ListState=Omit<PageState,'sort'> & {selected:Set<string>;mode:string;columnWidths:number[]|null;sort:PageState['sort']|null}
type ListConfig<Row>={key:string;label:string;itemLabel:string;signal:AbortSignal;rows:Row[];columns:ListColumn<Row>[];
 rowLabel:(row:Row)=>string; search?:(row:Row)=>string;includes?:(row:Row)=>boolean;toolbar?:boolean;selection?:boolean;action?:string;
 states?:Map<string,ListState>;initialQuery?:string;initialPage?:number;initialPageSize?:number;
 initialFilters?:Record<string,string>;defaultFilters?:Record<string,string>;defaultSort?:PageState['sort'];
 filters?:{key:string;label:string;options:Record<string,string>;matches?:(row:Row,value:string)=>boolean}[];
 loadPage?:RemotePage<Row>;onPaint?:(body:HTMLTableSectionElement,rows:Row[])=>void;onSelection?:(ids:string[])=>void}
const listStates=new Map<string,ListState>()
const collator=new Intl.Collator('zh-CN',{numeric:true})
const pageSizes=[10,20,50]
const selectionModes={none:'不选择',single:'单选',multiple:'多选'}
function options(values:Record<string,string>, selected:string|number) {
  return Object.entries(values).map(([value, label]) =>
    '<option value="' + escape(value) + '"' + (String(value) === String(selected) ? ' selected' : '') + '>' + escape(label) + '</option>'
  ).join('');
}

function getState<Row extends {id:string}>(config:ListConfig<Row>):ListState {
  const states = config.states ?? listStates;
  if (!states.has(config.key)) {
    const initial:ListState = {
      query: config.initialQuery ?? '', filters: { ...(config.initialFilters ?? config.defaultFilters) },
      page: config.initialPage ?? 0, pageSize: config.initialPageSize !== undefined && pageSizes.includes(config.initialPageSize) ? config.initialPageSize : pageSizes[0], selected: new Set(),
      sort: config.defaultSort ?? null, mode: config.selection === false ? 'none' : 'multiple', columnWidths: null
    };
    states.set(config.key, initial);
  }
  return states.get(config.key)!;
}

export function mountList<Row extends {id:string}>(root:HTMLElement,config:ListConfig<Row>) {
  const state = getState(config);
  const popoverId = 'list-options-' + config.key;
  const filters = config.filters ?? [];
  const optionsLabel = config.selection === false ? '筛选' : '筛选与选择';
  const queryLabel = '搜索' + config.itemLabel;
  const selectable = () => state.mode !== 'none';
  const headClass = (col:ListColumn<Row>) => col.secondary ? ' class="secondary"' : '';
  root.className = 'standard-list';
  root.setAttribute('aria-label', config.label);
  root.innerHTML = `
    <section class="list-toolbar" aria-label="列表工具栏" ${config.toolbar === false ? 'hidden' : ''}>
      <div class="list-toolbar-main">
        <input class="search" data-search type="search" aria-label="${queryLabel}" placeholder="${queryLabel}" value="${escape(state.query)}">
        <button class="button" type="button" data-more popovertarget="${popoverId}">${optionsLabel}</button>
      </div>
      <div class="list-toolbar-line" data-context hidden>
        <div class="list-selection">
          <span class="aux" data-filter-summary></span>
          <span class="aux" data-selection-count></span>
          <button class="button" type="button" data-clear-selection hidden>清除选择</button>
        </div>
        ${config.action ?? ''}
      </div>
    </section>
    <div class="list-scroll" role="region" aria-label="${config.label}列表内容" tabindex="0">
      <table aria-label="${config.label}">
        <thead><tr>
          <th class="selection-col" scope="col" data-choice-column>
            <label class="row-select" data-page-choice hidden><input type="checkbox" aria-label="选择当前页全部记录" data-all></label>
            <span class="sr-only" data-choice-label hidden>选择</span>
          </th>
          ${config.columns.map(col => '<th scope="col" data-column="' + col.key + '"' + headClass(col) + '>' + (col.sort ?
            '<button class="sort-button" type="button" data-sort="' + col.key + '">' + col.label + '<span class="sort-icon" aria-hidden="true">↕</span></button>' :
            '<span>' + col.label + '</span>') + '<button class="column-resizer" type="button" data-column-resizer aria-label="调整' + col.label +
            '及右侧列的宽度，按左右方向键" title="拖动调整相邻列宽"></button></th>').join('')}
        </tr></thead>
        <tbody></tbody>
      </table>
      <p class="empty" data-empty role="status" hidden></p>
    </div>
    <footer class="list-footer" aria-label="分页">
      <div class="list-count">
        <span data-total aria-live="polite"></span>
        <label><span class="sr-only">每页条数</span><select data-size>${options(Object.fromEntries(pageSizes.map(size => [size, '每页 ' + size + ' 条'])), state.pageSize)}</select></label>
      </div>
      <nav class="list-paging" aria-label="列表分页">
        <button class="button" type="button" data-prev>上一页</button><span data-page aria-live="polite"></span>
        <button class="button" type="button" data-next>下一页</button>
      </nav>
    </footer>
    <span class="sr-only" data-column-notice role="status"></span>
    <div class="list-popover" id="${popoverId}" popover="auto" aria-labelledby="${popoverId}-title">
      <h2 id="${popoverId}-title">${optionsLabel}</h2>
      ${filters.map(filter => '<label>' + escape(filter.label) + '<select class="select" aria-label="' + escape(filter.label) + '" data-filter="' + filter.key + '">' +
        options({ '': '全部', ...filter.options }, state.filters[filter.key] ?? '') + '</select></label>').join('')}
      ${config.selection === false ? '' : '<label>选择模式<select class="select" data-mode>' + options(selectionModes, state.mode) + '</select></label>'}
      <div class="popover-actions">
        <button class="button" type="button" data-reset-filters>重置筛选</button>
        <button class="button" type="button" popovertarget="${popoverId}" popovertargetaction="hide">收起</button>
      </div>
    </div>`;

  const find = (selector:string)=>requiredElement(root,selector,HTMLElement);
  const body = requiredElement(root,'tbody',HTMLTableSectionElement);
  const headerRow = find('thead tr');
  const headers = [...headerRow.querySelectorAll<HTMLElement>('[data-column]')];
  const table = requiredElement(root,'table',HTMLTableElement);
  const scroller = find('.list-scroll');
  const popover = find('[popover]');
  let visible:Row[] = [];

  function filteredRows() {
    const query = state.query.trim().toLocaleLowerCase();
    const results = (config.rows ?? []).filter(row =>
      (config.includes?.(row) ?? true) &&
      (!query || config.search?.(row).toLocaleLowerCase().includes(query)) &&
      filters.every(filter => {
        const value = state.filters[filter.key];
        return !value || (filter.matches ? filter.matches(row,value) : String(Object.entries(row).find(([key])=>key===filter.key)?.[1]) === value);
      })
    );
    const column = config.columns.find(col => col.key === state.sort?.key);
    const sort=column?.sort;
    if (typeof sort === 'function') {
      const factor = state.sort?.direction === 'desc' ? -1 : 1;
      results.sort((left, right) => factor * collator.compare(sort(left),sort(right)));
    }
    return results;
  }

  function updateSelection() {
    for (const row of body.rows) {
      const selected = state.selected.has(row.dataset.row ?? '');
      row.dataset.selected = String(selected);
      const control = row.querySelector<HTMLInputElement>('[data-row-select]');
      if (control) control.checked = selected;
    }
    const count = state.selected.size;
    const checked = visible.filter(row => state.selected.has(row.id)).length;
    const all = requiredElement(root,'[data-all]',HTMLInputElement);
    all.checked = checked > 0 && checked === visible.length;
    all.indeterminate = checked > 0 && checked < visible.length;
    all.disabled = !visible.length;
    find('[data-clear-selection]').hidden = !count;
    find('[data-selection-count]').textContent = count ? '已选 ' + count + ' 项' : '';
    config.onSelection?.([...state.selected]);
    const activeFilters = filters.filter(filter => state.filters[filter.key]);
    find('[data-filter-summary]').textContent = activeFilters.map(filter => filter.label + '：' + (filter.options[state.filters[filter.key]] ?? '当前筛选')).join(' · ');
    find('[data-context]').hidden = !(count || activeFilters.length || config.action);
  }

  let pending:AbortController|undefined;
  function paint({keepRows = false} = {}):void|Promise<boolean|void> {
    if (config.loadPage) {
      return loadPage(keepRows);
    }
    const rows = filteredRows();
    const existingIds = new Set((config.rows ?? []).map(row => row.id));
    for (const id of state.selected) {
      if (!existingIds.has(id)) state.selected.delete(id);
    }
    const pages = Math.ceil(rows.length / state.pageSize);
    state.page = Math.max(0, Math.min(state.page, pages - 1));
    paintRows(rows.slice(state.page * state.pageSize, (state.page + 1) * state.pageSize), rows.length);
  }

  async function loadPage(keepRows:boolean):Promise<boolean|void> {
    if(!config.loadPage)return;
    pending?.abort();
    pending = new AbortController();
    const signal = AbortSignal.any([config.signal, pending.signal]);
    const empty = find('[data-empty]');
    let loading;
    if (!keepRows || !visible.length) {
      body.replaceChildren();
      visible = [];
      empty.hidden = false;
      find('[data-page]').textContent = '';
      find('[data-total]').textContent = '';
      requiredElement(root,'[data-prev]',HTMLButtonElement).disabled = requiredElement(root,'[data-next]',HTMLButtonElement).disabled = true;
      find('[data-choice-column]').hidden = !selectable();
      loading = mountLoading(empty, {label:'正在读取' + config.itemLabel + '…'});
    }
    try {
      const result = await config.loadPage({...state,sort:state.sort}, signal);
      if (signal.aborted) return;
      for (const filter of filters) {
        const values = result.options?.[filter.key];
        if (!values) continue;
        filter.options = values;
        const selected = state.filters[filter.key] || '';
        find('[data-filter="' + filter.key + '"]').innerHTML = options({'':'全部', ...values,
          ...(selected && !Object.hasOwn(values, selected) ? {[selected]: '当前筛选'} : {})}, selected);
      }
      const last = Math.max(0, Math.ceil(result.total / state.pageSize) - 1);
      if (state.page > last) {
        state.page = last;
        return paint();
      }
      const scrollTop = scroller.scrollTop;
      paintRows(result.items, result.total);
      if (keepRows) scroller.scrollTop = scrollTop;
      return true;
    } catch (error) {
      if (signal.aborted) return;
      body.replaceChildren();
      visible = [];
      empty.hidden = false;
      find('[data-page]').textContent = '';
      find('[data-total]').textContent = '';
      requiredElement(root,'[data-prev]',HTMLButtonElement).disabled = requiredElement(root,'[data-next]',HTMLButtonElement).disabled = true;
      for (const filter of filters) {
        filter.options = {};
        const selected = state.filters[filter.key] || '';
        find('[data-filter="' + filter.key + '"]').innerHTML = options(selected ? {'':'全部', [selected]:'当前筛选'} : {'':'全部'}, selected);
      }
      updateSelection();
      empty.textContent = error instanceof ApiError && error.status===403 ? '当前账号无权读取此列表。' : '列表读取失败。';
      const retry = document.createElement('button');
      retry.type = 'button';
      retry.className = 'button';
      retry.textContent = '重新读取';
      retry.onclick = () => { void paint() };
      empty.append(' ', retry);
      return false;
    } finally {
      loading?.finish();
    }
  }

  function paintRows(rows:Row[], total:number) {
    const pageCount = Math.ceil(total / state.pageSize);
    visible = rows;
    const showSelection = selectable();
    find('[data-choice-column]').hidden = !showSelection;
    find('[data-page-choice]').hidden = state.mode !== 'multiple';
    find('[data-choice-label]').hidden = state.mode !== 'single';
    for (const cell of headers) {
      if (cell.dataset.column === state.sort?.key) {
        cell.setAttribute('aria-sort', state.sort?.direction === 'desc' ? 'descending' : 'ascending');
      } else cell.removeAttribute('aria-sort');
      const arrow = cell.querySelector('.sort-icon');
      if (arrow) arrow.textContent = cell.dataset.column !== state.sort?.key ? '↕' : state.sort?.direction === 'desc' ? '↓' : '↑';
    }
    body.innerHTML = visible.map(row => '<tr data-row="' + escape(row.id) + '">' +
      (showSelection ? '<td class="selection-col"><label class="row-select"><input data-row-select type="' +
        (state.mode === 'single' ? 'radio' : 'checkbox') + '" name="list-selection" value="' + escape(row.id) +
        '" aria-label="选择 ' + escape(config.rowLabel(row)) + '"></label></td>' : '') +
      config.columns.map(col => '<td' + headClass(col) + '>' + col.render(row) + '</td>').join('') + '</tr>').join('');
    const empty = find('[data-empty]');
    empty.hidden = Boolean(rows.length);
    empty.textContent = (config.rows?.length || (config.loadPage && (state.query || Object.values(state.filters).some(Boolean)))) ? '没有匹配的' + config.itemLabel + '。' : '暂无' + config.itemLabel + '。';
    find('[data-total]').textContent = '共 ' + total + ' 条';
    find('[data-page]').textContent = '第 ' + (pageCount ? state.page + 1 : 0) + ' / ' + pageCount + ' 页';
    requiredElement(root,'[data-prev]',HTMLButtonElement).disabled = state.page === 0;
    requiredElement(root,'[data-next]',HTMLButtonElement).disabled = state.page + 1 >= pageCount;
    scroller.scrollTop = 0;
    updateSelection();
    config.onPaint?.(body,rows);
  }

  function applyColumnWidths() {
    const widths = state.columnWidths;
    let total = 0;
    let compactTotal = 0;
    headers.forEach((header, index) => {
      const width = widths?.[index] ?? 0;
      header.style.setProperty('--list-column-width', widths ? width + 'px' : '');
      if (!widths) return;
      total += width;
      if (!config.columns[index].secondary) compactTotal += width;
    });
    table.style.setProperty('--list-columns-width', widths ? total + 'px' : '');
    table.style.setProperty('--list-columns-compact-width', widths ? compactTotal + 'px' : '');
    table.toggleAttribute('data-sized-columns', Boolean(widths));
  }

  function measureColumnWidths() {
    // Secondary columns can be hidden by the current viewport.
    return headers.map((header, index) => header.getBoundingClientRect().width || (state.columnWidths?.[index] ?? 144));
  }

  function adjacentColumns(handle:HTMLElement) {
    const left = handle.parentElement ? headers.indexOf(handle.parentElement) : -1;
    const right = headers.findIndex((header, index) => index > left && header.getClientRects().length > 0);
    return right < 0 ? null : { left, right };
  }

  function resizeColumns(widths:number[],{left,right}:Pair,delta:number) {
    const leftWidth = widths[left];
    const rightWidth = widths[right];
    const shrinkLeft = Math.max(0, leftWidth - 64);
    const shrinkRight = Math.max(0, rightWidth - 64);
    const offset = Math.max(-shrinkLeft, Math.min(shrinkRight, delta));
    state.columnWidths = [...widths];
    state.columnWidths[left] = leftWidth + offset;
    state.columnWidths[right] = rightWidth - offset;
    applyColumnWidths();
  }

  function announceColumnWidths({left,right}:Pair) {
    find('[data-column-notice]').textContent = [left, right].map(index =>
      config.columns[index].label + '列宽 ' + Math.round(state.columnWidths?.[index] ?? 0) + ' 像素'
    ).join('，');
  }

  headerRow.addEventListener('keydown', event => {
    const handle = closestControl(event,'[data-column-resizer]');
    if (!handle || !['ArrowLeft', 'ArrowRight'].includes(event.key)) return;
    const pair = adjacentColumns(handle);
    if (!pair) return;
    event.preventDefault();
    const delta = event.key === 'ArrowRight' ? 10 : -10;
    resizeColumns(measureColumnWidths(), pair, delta);
    announceColumnWidths(pair);
  });

  headerRow.addEventListener('pointerdown', event => {
    const handle = closestControl(event,'[data-column-resizer]');
    if (!handle || !event.isPrimary || event.button !== 0) return;
    const pair = adjacentColumns(handle);
    if (!pair) return;
    const { pointerId, clientX: startX } = event;
    const previousWidths = state.columnWidths;
    const widths = measureColumnWidths();
    const listeners = new AbortController();
    const options = { signal: config.signal ? AbortSignal.any([listeners.signal,config.signal]) : listeners.signal };
    let moved = false;

    const update = (next:PointerEvent) => {
      if (next.pointerId !== pointerId) return;
      const delta = next.clientX - startX;
      if (!delta && !moved) return;
      moved = true;
      resizeColumns(widths, pair, delta);
    }

    const finish = (commit:boolean) => {
      listeners.abort();
      delete handle.dataset.resizing;
      if (handle.hasPointerCapture(pointerId)) handle.releasePointerCapture(pointerId);
      if (!moved) return;
      if (commit) {
        announceColumnWidths(pair);
      } else {
        state.columnWidths = previousWidths;
        applyColumnWidths();
      }
    }

    handle.dataset.resizing = '';
    handle.setPointerCapture(pointerId);
    handle.addEventListener('pointermove', update, options);
    handle.addEventListener('pointerup', next => {
      if (next.pointerId !== pointerId) return;
      update(next);
      finish(true);
    }, options);
    handle.addEventListener('lostpointercapture', next => {
      if (next.pointerId === pointerId) finish(false);
    }, options);
    window.addEventListener('keydown', next => {
      if (next.key === 'Escape') finish(false);
    }, options);
    window.addEventListener('blur', () => finish(false), options);
  });

  function placePopover() {
    if (!popover.isConnected) return;
    const button = find('[data-more]').getBoundingClientRect();
    const width = popover.offsetWidth;
    const height = popover.offsetHeight;
    const below = button.bottom + 6;
    const left = Math.max(10, Math.min(button.right - width, innerWidth - width - 10));
    const top = below + height <= innerHeight - 10 ? below : Math.max(10, button.top - height - 6);
    popover.style.inset = top + 'px auto auto ' + left + 'px';
    popover.style.position = 'fixed';
  }

  popover.addEventListener('beforetoggle', event => {
    if (event.newState === 'open') requestAnimationFrame(placePopover);
  });
  root.addEventListener('input', event => {
    if (!(event.target instanceof HTMLInputElement) || !event.target.matches('[data-search]')) return;
    state.query = event.target.value;
    state.page = 0;
    state.selected.clear();
    paint();
  }, {signal:config.signal});
  root.addEventListener('change', event => {
    const control = event.target;
    if(!(control instanceof HTMLInputElement || control instanceof HTMLSelectElement))return;
    if (control.matches('[data-row-select]')) {
      if (state.mode === 'single') state.selected.clear();
      if (control instanceof HTMLInputElement && control.checked) state.selected.add(control.value);
      else state.selected.delete(control.value);
      updateSelection();
      return;
    }
    if (control.matches('[data-all]')) {
      for (const row of visible) {
        if (control instanceof HTMLInputElement && control.checked) state.selected.add(row.id);
        else state.selected.delete(row.id);
      }
      updateSelection();
      return;
    }
    if (!control.matches('[data-size], [data-mode], [data-filter]')) return;
    if (control.matches('[data-size]')) {
      state.pageSize = Number(control.value);
      state.page = 0;
    }
    if (control.matches('[data-mode]')) {
      state.mode = control.value;
      state.selected.clear();
    }
    if (control.matches('[data-filter]')) {
      state.filters[control.dataset.filter ?? ''] = control.value;
      state.page = 0;
      state.selected.clear();
    }
    paint();
  }, {signal:config.signal});
  root.addEventListener('click', event => {
    const button = closestControl(event,'button');
    if (!button) return;
    if (button.matches('[data-clear-selection]')) {
      state.selected.clear();
      find('[data-more]').focus();
      updateSelection();
      toast('info', '已清除所选记录');
      return;
    }
    if (button.matches('[data-reset-filters]')) {
      state.query = '';
      state.filters = { ...config.defaultFilters };
      state.page = 0;
      state.selected.clear();
      requiredElement(root,'[data-search]',HTMLInputElement).value = '';
      root.querySelectorAll<HTMLSelectElement>('[data-filter]').forEach(field => { field.value = state.filters[field.dataset.filter ?? ''] ?? ''; });
      paint();
    }
    if (button.matches('[data-sort]')) {
      const key = button.dataset.sort;
      if(!key)return;
      const direction = state.sort?.key === key && state.sort?.direction === 'asc' ? 'desc' : 'asc';
      state.sort = { key, direction };
      state.page = 0;
      paint();
    }
    if (button.matches('[data-prev], [data-next]')) {
      state.page += button.matches('[data-prev]') ? -1 : 1;
      paint();
      if (button instanceof HTMLButtonElement && button.disabled) find(button.matches('[data-prev]') ? '[data-next]' : '[data-prev]').focus();
    }
  }, {signal:config.signal});

  applyColumnWidths();
  paint();
  return { refresh: paint, replacePage(items:Row[],total:number) {
    if (!items.length && state.page > 0) {
      state.page = Math.max(0,Math.ceil(total/state.pageSize)-1);
      return paint();
    }
    paintRows(items,total);
  }, resetPage() { state.page = 0; paint(); } };
}
