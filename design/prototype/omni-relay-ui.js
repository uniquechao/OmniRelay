/* Shared list page and toast behaviors for the OmniRelay workbench prototype. */
window.OmniRelayUI = (() => {
  const listStates = new Map();
  const collator = new Intl.Collator('zh-CN', { numeric: true });
  const pageSizes = [10, 20, 50];
  const selectionModes = { none: '不选择', single: '单选', multiple: '多选' };
  const tipLevels = { info: '信息', success: '成功', warning: '警告', error: '错误' };
  const tipTimers = new WeakMap();
  let dialogNumber = 0;
  let treeNumber = 0;

  function escape(value) {
    return String(value).replace(/[&<>"']/g, char => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    })[char]);
  }

  const avatarTones = ['indigo','teal','plum','amber','blue'];

  function fileSourceLocator(fileId, group, route, ordinal) {
    const name = escape(group);
    const label = '定位原消息：' + name + '（来源 ' + ordinal + '）';
    return '<div class="drive-source-row"><strong>' + name + '</strong>' +
      '<a class="drive-source drive-source-locate" data-source-file="' + escape(fileId) + '" href="' + escape(route) +
      '" aria-label="' + label + '" title="' + label + '">' +
      '<svg aria-hidden="true" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">' +
      '<circle cx="12" cy="12" r="7"/><circle cx="12" cy="12" r="2"/><path d="M12 2v3m0 14v3M2 12h3m14 0h3"/></svg></a></div>';
  }

  function personAvatar(label, id = label) {
    const identity = id.normalize('NFKC').trim();
    let hash = 2166136261;
    for (const character of identity) hash = Math.imul(hash ^ character.codePointAt(0),16777619);
    const tone = avatarTones[(hash >>> 0) % avatarTones.length];
    return '<span class="person-avatar" data-avatar-tone="' + tone + '" aria-hidden="true">' +
      escape([...label].slice(-2).join('')) + '</span>';
  }

  function personDetails(person) {
    return {
      name: person.name?.trim() || '未知人员',
      key: person.person_key ?? person.personKey,
      org: person.organization_paths?.map(path => path.join(' / ')).join('；') ?? person.org ?? '',
      source: person.source === 'feishu' ? '飞书' : person.source ?? ''
    };
  }

  function personButton(person, content, className = '') {
    const { name, key, org, source } = personDetails(person);
    const resigned = person.resigned === true;
    if (resigned && className === 'person-name') content += '<small class="person-resigned">离职</small>';
    return '<button type="button" class="person-button ' + escape(className) +
      '" data-person-name="' + escape(name) + '" data-person-key="' + escape(key ?? '') +
      '" data-person-org="' + escape(org) + '" data-person-source="' + escape(source) + '"' +
      (resigned ? ' data-person-resigned="true"' : '') +
      ' aria-haspopup="dialog" aria-expanded="false" aria-label="查看' + escape(name) + (resigned ? '（离职）' : '') +
      '的基本信息" title="' + escape(name) + '">' + content + '</button>';
  }

  function personOrganization(org) {
    return org ? '<span class="person-org" title="' + escape(org) + '">' + escape(org) + '</span>' : '';
  }

  function personView(person) {
    const { name, key, org } = personDetails(person);
    const label = escape(name);
    return '<span class="person-view person-compact">' +
      personButton(person,personAvatar(name,key || name),'person-portrait') +
      '<span class="person-copy">' + personButton(person,label,'person-name') +
      personOrganization(org) + '</span></span>';
  }

  function mountPersonCards(root, { signal } = {}) {
    let active;
    function dismiss() {
      active?.close();
    }
    signal?.addEventListener('abort',dismiss,{ once:true });
    root.addEventListener('click',event => {
      const trigger = event.target.closest('[data-person-name]');
      if (!trigger) return;
      const alreadyOpen = active?.card.matches(':popover-open') && active.trigger === trigger;
      dismiss();
      if (alreadyOpen) return;

      const { personName:name, personKey:key, personOrg:org, personSource:source } = trigger.dataset;
      const card = document.createElement('div');
      card.className = 'person-popover';
      card.popover = 'auto';
      card.tabIndex = -1;
      card.setAttribute('role','dialog');
      card.setAttribute('aria-label',name + '的基本信息');
      card.innerHTML = '<div class="person-view">' + personAvatar(name,key || name) +
        '<div class="person-copy"><strong class="person-name" title="' + escape(name) + '">' + escape(name) +
        '</strong>' + (trigger.dataset.personResigned === 'true' ?
          '<small class="person-resigned">' + icon('status-danger') + '离职</small>' : '') +
        personOrganization(org) + '</div></div>' +
        (source ? '<p class="aux">身份来源　' + escape(source) + '</p>' : '');
      const ownerDialog = trigger.closest('dialog');
      (ownerDialog ?? root).append(card);

      const tracking = new AbortController();
      active = {
        card, trigger,
        close() {
          if (card.matches(':popover-open')) card.hidePopover();
          else if (!card.isConnected) {
            tracking.abort();
            observer.disconnect();
            active = undefined;
          }
        }
      };
      const viewport = window.visualViewport;
      function position() {
        if (!trigger.getClientRects().length) { dismiss(); return; }
        const anchor = trigger.getBoundingClientRect();
        const leftEdge = viewport?.offsetLeft ?? 0;
        const topEdge = viewport?.offsetTop ?? 0;
        const rightEdge = leftEdge + (viewport?.width ?? document.documentElement.clientWidth);
        const bottomEdge = topEdge + (viewport?.height ?? document.documentElement.clientHeight);
        const inset = 8;
        card.style.maxWidth = Math.max(0,rightEdge - leftEdge - inset * 2) + 'px';
        card.style.maxHeight = Math.max(0,bottomEdge - topEdge - inset * 2) + 'px';
        const width = card.offsetWidth;
        const height = card.offsetHeight;
        const below = anchor.bottom + 6;
        const above = anchor.top - height - 6;
        const top = below + height <= bottomEdge - inset ? below : above;
        card.style.left = Math.max(leftEdge + inset, Math.min(anchor.left,rightEdge - width - inset)) + 'px';
        card.style.top = Math.max(topEdge + inset, Math.min(top,bottomEdge - height - inset)) + 'px';
      }

      const observer = new MutationObserver(() => {
        if (!root.contains(trigger)) dismiss();
      });
      observer.observe(root,{ childList:true, subtree:true });
      window.addEventListener('resize',position,{ signal:tracking.signal });
      window.addEventListener('scroll',position,{ capture:true, signal:tracking.signal });
      window.addEventListener('hashchange',dismiss,{ signal:tracking.signal });
      ownerDialog?.addEventListener('close',dismiss,{ signal:tracking.signal });
      viewport?.addEventListener('resize',position,{ signal:tracking.signal });
      viewport?.addEventListener('scroll',position,{ signal:tracking.signal });
      card.addEventListener('beforetoggle',event => {
        if (event.newState !== 'closed') return;
        tracking.abort();
        observer.disconnect();
        trigger.setAttribute('aria-expanded','false');
      });
      card.addEventListener('toggle',event => {
        if (event.newState !== 'closed') return;
        card.remove();
        if (active?.card !== card) return;
        active = undefined;
        const focusedOverlay = document.activeElement?.closest(':popover-open, dialog[open]');
        if (!signal?.aborted && trigger.getClientRects().length && (!focusedOverlay || focusedOverlay === ownerDialog)) {
          trigger.focus({ preventScroll:true });
        }
      });

      card.showPopover({ source:trigger });
      position();
      trigger.setAttribute('aria-expanded','true');
      card.focus({ preventScroll:true });
    },{ signal });
    return dismiss;
  }

  const icon = (name, className = 'icon') =>
    '<svg class="' + className + '" aria-hidden="true"><use href="#' + name + '"></use></svg>';

  function openDialog({title, description, body, variant = 'detail', tone = 'default',
    confirm, cancelLabel, discard, onSubmit, onClose, restoreFocus, drawer, isDirty}) {
    const base = 'workspace-dialog-' + ++dialogNumber;
    const dialog = document.createElement('dialog');
    const symbol = tone === 'danger' ? 'status-danger' : tone === 'warning' ? 'status-warning' : 'nav-logs';
    const actionColor = tone === 'default' ? 'primary' : 'dialog-confirm-' + tone;
    dialog.className = 'or-dialog' + (drawer ? ' or-drawer' : '');
    if (drawer) dialog.dataset.drawerWidth = drawer;
    dialog.dataset.variant = variant;
    dialog.dataset.tone = tone;
    dialog.setAttribute('aria-labelledby',base + '-title');
    if (description) dialog.setAttribute('aria-describedby',base + '-description');
    dialog.innerHTML = `<form class="dialog-form" method="dialog">
      <header class="dialog-head">
        <span class="dialog-symbol">${icon(symbol)}</span>
        <div class="dialog-heading"><h2 id="${base}-title">${escape(title)}</h2>
          ${description ? '<p id="' + base + '-description">' + escape(description) + '</p>' : ''}
        </div>
        <button class="dialog-close" type="button" data-dismiss aria-label="关闭对话框">${icon('action-close')}</button>
      </header>
      <div class="dialog-body">${body}
        <p class="field-error" id="${base}-error" role="alert" hidden></p>
      </div>
      <footer class="dialog-footer">
        <button class="button dialog-secondary" type="button" data-dismiss${variant === 'form' ? '' : ' autofocus'}>${escape(cancelLabel || (confirm ? '取消' : '关闭'))}</button>
        ${discard ? '<button class="button dialog-secondary" type="button" data-discard>' + escape(discard) + '</button>' : ''}
        ${confirm ? '<button class="button ' + actionColor + '" type="submit" value="confirm">' + escape(confirm) + '</button>' : ''}
      </footer>
    </form>`;
    const form = dialog.querySelector('form');
    const message = dialog.querySelector('.field-error');
    let invalidField;
    let guard;
    let closeAfterSubmit = false;
    let lastField;
    dialog.querySelector('.dialog-body').addEventListener('focusin',event => {
      if (event.target.matches('input, select, textarea')) lastField = event.target;
    });
    function requestClose() {
      if (!isDirty?.(form)) { dialog.close('cancel'); return; }
      if (guard?.open) return;
      const nextGuard = openDialog({
        title:'保存修改？', variant:'confirm', tone:'warning',
        body:'<p class="dialog-note">有未保存的修改，直接关闭会丢失本次更改。</p>',
        cancelLabel:'继续编辑', discard:'不保存', confirm:'保存并关闭',
        onClose(confirmed, result) {
          if (guard !== nextGuard) return;
          guard = undefined;
          if (!dialog.open) return;
          if (confirmed) {
            closeAfterSubmit = true;
            form.requestSubmit();
            closeAfterSubmit = false;
          }
          else if (result === 'discard') dialog.close('cancel');
          else {
            dialog.dispatchEvent(new Event('or-dialog-guard-cancel'));
            lastField?.focus();
          }
        }
      });
      guard = nextGuard;
    }
    const clearError = () => {
      if (!invalidField) return;
      invalidField.removeAttribute('aria-invalid');
      invalidField.removeAttribute('aria-describedby');
      message.hidden = true;
      invalidField = undefined;
    };
    form.addEventListener('input',clearError);
    form.addEventListener('reset',clearError);
    form.addEventListener('submit',async event => {
      event.preventDefault();
      if (!confirm && !onSubmit) return;
      clearError();
      const submission = onSubmit?.(form,event.submitter);
      const result = submission instanceof Promise ? await submission : submission;
      if (!dialog.open) return;
      if (result?.keepOpen) {
        if (closeAfterSubmit) dialog.close('confirm');
        return;
      }
      if (result) {
        invalidField = result.field;
        message.hidden = false;
        message.textContent = result.message;
        invalidField.setAttribute('aria-invalid','true');
        invalidField.setAttribute('aria-describedby',message.id);
        invalidField.focus();
        return;
      }
      dialog.close('confirm');
    });
    dialog.querySelectorAll('[data-dismiss]').forEach(button => {
      button.addEventListener('click',requestClose);
    });
    dialog.querySelector('[data-discard]')?.addEventListener('click',() => dialog.close('discard'));
    dialog.addEventListener('cancel',event => { event.preventDefault(); requestClose(); });
    if (drawer) {
      let startedOutside = false;
      const outside = event => {
        const box = dialog.getBoundingClientRect();
        return event.target === dialog && (event.clientX < box.left || event.clientX >= box.right ||
          event.clientY < box.top || event.clientY >= box.bottom);
      };
      dialog.addEventListener('pointerdown',event => { startedOutside = outside(event); });
      dialog.addEventListener('pointercancel',() => { startedOutside = false; });
      dialog.addEventListener('click',event => { if (startedOutside && outside(event)) requestClose(); });
    }
    dialog.addEventListener('close',() => {
      guard?.close();
      const confirmed = dialog.returnValue === 'confirm';
      dialog.remove();
      onClose?.(confirmed,dialog.returnValue);
      restoreFocus?.(confirmed)?.focus();
    },{once:true});
    document.body.append(dialog);
    dialog.showModal();
    return dialog;
  }

  function toast(level, message) {
    const region = document.getElementById('toast-region');
    const item = document.createElement('aside');
    item.className = 'toast';
    item.dataset.level = level;
    item.innerHTML = '<strong aria-hidden="true">' + tipLevels[level] + '</strong>' +
      '<span>' + escape(message) + '</span><button type="button" aria-label="关闭提示">×</button>';
    const dismiss = () => { clearTimeout(tipTimers.get(item)); item.remove(); };
    const schedule = () => {
      clearTimeout(tipTimers.get(item));
      tipTimers.set(item, setTimeout(dismiss, 6000));
    };
    item.querySelector('button').addEventListener('click', dismiss);
    item.addEventListener('pointerenter', () => clearTimeout(tipTimers.get(item)));
    item.addEventListener('pointerleave', () => { if (!item.contains(document.activeElement)) schedule(); });
    item.addEventListener('focusin', () => clearTimeout(tipTimers.get(item)));
    item.addEventListener('focusout', () => { if (!item.matches(':hover')) schedule(); });
    if (region.children.length >= 3) {
      const oldest = region.firstElementChild;
      clearTimeout(tipTimers.get(oldest));
      oldest.remove();
    }
    region.append(item);
    const live = document.getElementById(level === 'error' ? 'toast-urgent' : 'toast-status');
    live.textContent = '';
    requestAnimationFrame(() => { live.textContent = tipLevels[level] + '，' + message; });
    schedule();
  }

  function options(values, selected) {
    return Object.entries(values).map(([value, label]) =>
      '<option value="' + escape(value) + '"' + (String(value) === String(selected) ? ' selected' : '') + '>' + escape(label) + '</option>'
    ).join('');
  }

  function getState(config) {
    const states = config.states ?? listStates;
    if (!states.has(config.key)) {
      const initial = {
        query: config.initialQuery ?? '', filters: { ...(config.initialFilters ?? config.defaultFilters) },
        page: config.initialPage ?? 0, pageSize: pageSizes.includes(config.initialPageSize) ? config.initialPageSize : pageSizes[0], selected: new Set(),
        sort: config.defaultSort ?? null, mode: config.selection === false ? 'none' : 'multiple', columnWidths: null
      };
      states.set(config.key, initial);
    }
    return states.get(config.key);
  }

  function mountList(root, config) {
    const state = getState(config);
    const popoverId = 'list-options-' + config.key;
    const filters = config.filters ?? [];
    const optionsLabel = config.selection === false ? '筛选' : '筛选与选择';
    const queryLabel = '搜索' + config.itemLabel;
    const selectable = () => state.mode !== 'none';
    const headClass = col => col.secondary ? ' class="secondary"' : '';
    root.className = 'standard-list';
    root.setAttribute('aria-label', config.label);
    root.innerHTML = `
      <section class="list-toolbar" aria-label="列表工具栏">
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

    const find = selector => root.querySelector(selector);
    const body = find('tbody');
    const headerRow = find('thead tr');
    const headers = [...headerRow.querySelectorAll('[data-column]')];
    const table = find('table');
    const scroller = find('.list-scroll');
    const popover = find('[popover]');
    let visible = [];

    function filteredRows() {
      const query = state.query.trim().toLocaleLowerCase();
      const results = config.rows.filter(row =>
        (config.includes?.(row) ?? true) &&
        (!query || config.search(row).toLocaleLowerCase().includes(query)) &&
        filters.every(filter => {
          const value = state.filters[filter.key];
          return !value || (filter.matches ? filter.matches(row,value) : String(row[filter.key]) === value);
        })
      );
      const column = config.columns.find(col => col.key === state.sort?.key);
      if (column?.sort) {
        const factor = state.sort.direction === 'desc' ? -1 : 1;
        results.sort((left, right) => factor * collator.compare(column.sort(left), column.sort(right)));
      }
      return results;
    }

    function updateSelection() {
      for (const row of body.rows) {
        const selected = state.selected.has(row.dataset.row);
        row.dataset.selected = String(selected);
        const control = row.querySelector('[data-row-select]');
        if (control) control.checked = selected;
      }
      const count = state.selected.size;
      const checked = visible.filter(row => state.selected.has(row.id)).length;
      const all = find('[data-all]');
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

    let pending;
    function paint({keepRows = false} = {}) {
      if (config.loadPage) {
        return loadPage(keepRows);
      }
      const rows = filteredRows();
      const existingIds = new Set(config.rows.map(row => row.id));
      for (const id of state.selected) {
        if (!existingIds.has(id)) state.selected.delete(id);
      }
      const pages = Math.ceil(rows.length / state.pageSize);
      state.page = Math.max(0, Math.min(state.page, pages - 1));
      paintRows(rows.slice(state.page * state.pageSize, (state.page + 1) * state.pageSize), rows.length);
    }

    async function loadPage(keepRows) {
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
        find('[data-prev]').disabled = find('[data-next]').disabled = true;
        find('[data-choice-column]').hidden = !selectable();
        loading = window.OmniRelayLoading.mount(empty, {label:'正在读取' + config.itemLabel + '…'});
      }
      try {
        const result = await config.loadPage(state, signal);
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
        find('[data-prev]').disabled = find('[data-next]').disabled = true;
        for (const filter of filters) {
          filter.options = {};
          const selected = state.filters[filter.key] || '';
          find('[data-filter="' + filter.key + '"]').innerHTML = options(selected ? {'':'全部', [selected]:'当前筛选'} : {'':'全部'}, selected);
        }
        updateSelection();
        empty.textContent = error.status === 403 ? '当前账号无权读取此列表。' : '列表读取失败。';
        const retry = document.createElement('button');
        retry.type = 'button';
        retry.className = 'button';
        retry.textContent = '重新读取';
        retry.onclick = paint;
        empty.append(' ', retry);
        return false;
      } finally {
        loading?.finish();
      }
    }

    function paintRows(rows, total) {
      const pageCount = Math.ceil(total / state.pageSize);
      visible = rows;
      const showSelection = selectable();
      find('[data-choice-column]').hidden = !showSelection;
      find('[data-page-choice]').hidden = state.mode !== 'multiple';
      find('[data-choice-label]').hidden = state.mode !== 'single';
      for (const cell of headers) {
        if (cell.dataset.column === state.sort?.key) {
          cell.setAttribute('aria-sort', state.sort.direction === 'desc' ? 'descending' : 'ascending');
        } else cell.removeAttribute('aria-sort');
        const arrow = cell.querySelector('.sort-icon');
        if (arrow) arrow.textContent = cell.dataset.column !== state.sort?.key ? '↕' : state.sort.direction === 'desc' ? '↓' : '↑';
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
      find('[data-prev]').disabled = state.page === 0;
      find('[data-next]').disabled = state.page + 1 >= pageCount;
      scroller.scrollTop = 0;
      updateSelection();
      config.onPaint?.(body,rows);
    }

    function applyColumnWidths() {
      const widths = state.columnWidths;
      let total = 0;
      let compactTotal = 0;
      headers.forEach((header, index) => {
        const width = widths?.[index];
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

    function adjacentColumns(handle) {
      const left = headers.indexOf(handle.parentElement);
      const right = headers.findIndex((header, index) => index > left && header.getClientRects().length > 0);
      return right < 0 ? null : { left, right };
    }

    function resizeColumns(widths, { left, right }, delta) {
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

    function announceColumnWidths({ left, right }) {
      find('[data-column-notice]').textContent = [left, right].map(index =>
        config.columns[index].label + '列宽 ' + Math.round(state.columnWidths[index]) + ' 像素'
      ).join('，');
    }

    headerRow.addEventListener('keydown', event => {
      const handle = event.target.closest('[data-column-resizer]');
      if (!handle || !['ArrowLeft', 'ArrowRight'].includes(event.key)) return;
      const pair = adjacentColumns(handle);
      if (!pair) return;
      event.preventDefault();
      const delta = event.key === 'ArrowRight' ? 10 : -10;
      resizeColumns(measureColumnWidths(), pair, delta);
      announceColumnWidths(pair);
    });

    headerRow.addEventListener('pointerdown', event => {
      const handle = event.target.closest('[data-column-resizer]');
      if (!handle || !event.isPrimary || event.button !== 0) return;
      const pair = adjacentColumns(handle);
      if (!pair) return;
      const { pointerId, clientX: startX } = event;
      const previousWidths = state.columnWidths;
      const widths = measureColumnWidths();
      const listeners = new AbortController();
      const options = { signal: config.signal ? AbortSignal.any([listeners.signal,config.signal]) : listeners.signal };
      let moved = false;

      function update(next) {
        if (next.pointerId !== pointerId) return;
        const delta = next.clientX - startX;
        if (!delta && !moved) return;
        moved = true;
        resizeColumns(widths, pair, delta);
      }

      function finish(commit) {
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
      if (!event.target.matches('[data-search]')) return;
      state.query = event.target.value;
      state.page = 0;
      state.selected.clear();
      paint();
    }, {signal:config.signal});
    root.addEventListener('change', event => {
      const control = event.target;
      if (control.matches('[data-row-select]')) {
        if (state.mode === 'single') state.selected.clear();
        if (control.checked) state.selected.add(control.value);
        else state.selected.delete(control.value);
        updateSelection();
        return;
      }
      if (control.matches('[data-all]')) {
        for (const row of visible) {
          if (control.checked) state.selected.add(row.id);
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
        state.filters[control.dataset.filter] = control.value;
        state.page = 0;
        state.selected.clear();
      }
      paint();
    }, {signal:config.signal});
    root.addEventListener('click', event => {
      const button = event.target.closest('button');
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
        find('[data-search]').value = '';
        root.querySelectorAll('[data-filter]').forEach(field => { field.value = state.filters[field.dataset.filter] ?? ''; });
        paint();
      }
      if (button.matches('[data-sort]')) {
        const key = button.dataset.sort;
        const direction = state.sort?.key === key && state.sort.direction === 'asc' ? 'desc' : 'asc';
        state.sort = { key, direction };
        state.page = 0;
        paint();
      }
      if (button.matches('[data-prev], [data-next]')) {
        state.page += button.matches('[data-prev]') ? -1 : 1;
        paint();
        if (button.disabled) find(button.matches('[data-prev]') ? '[data-next]' : '[data-prev]').focus();
      }
    }, {signal:config.signal});

    applyColumnWidths();
    paint();
    return { refresh: paint, replacePage(items,total) {
      if (!items.length && state.page > 0) {
        state.page = Math.max(0,Math.ceil(total/state.pageSize)-1);
        return paint();
      }
      paintRows(items,total);
    }, resetPage() { state.page = 0; paint(); } };
  }

  function mountOrganizationTree(host, departments, {
    initialId = '', onChange, includeAll = true, selectBranches = true, loadChildren, onLoadError
  } = {}) {
    const byParent = new Map();
    const departmentIds = new Set(departments.map(item => String(item.id)));
    const rendered = new Set();
    const included = new Map();
    let sequence = 0;
    const prefix = 'organization-tree-' + ++treeNumber;
    const rootKey = prefix + '-root';
    const rootLabel = includeAll ? '全部组织' : 'OmniRelay';
    if (includeAll) included.set('', { id:'', path:rootLabel });
    for (const item of departments) {
      const parent = departmentIds.has(String(item.parentId)) ? String(item.parentId) : '';
      if (!byParent.has(parent)) byParent.set(parent, []);
      byParent.get(parent).push(item);
    }

    const pick = (id, label, path, symbol) =>
      '<button class="tree-choice" type="button" data-org="' + escape(id) + '" aria-pressed="false"' +
      ' title="' + escape(path) + '">' + icon(symbol) + '<span class="tree-node-label">' + escape(label) + '</span>' +
      '<span data-chosen aria-hidden="true">✓</span></button>';
    const toggle = (key, label, open, symbol) => {
      const action = (open ? '收起' : '展开') + label;
      return '<button class="tree-toggle' + (symbol ? ' tree-branch-label' : '') + '" type="button" data-tree-toggle="' +
        escape(label) + '" aria-label="' + escape(action) + '" title="' + escape(action) + '" aria-controls="' + key +
        '" aria-expanded="' + open + '">' + icon('action-expand','icon tree-expander') +
        (symbol ? icon(symbol) + '<span class="tree-node-label">' + escape(label) + '</span>' : '') + '</button>';
    };
    function branch(item, ancestors, open) {
      const id = String(item.id);
      if (rendered.has(id)) return '';
      rendered.add(id);
      const path = [...ancestors, item.name];
      const descendants = new Set([id]);
      included.set(id, { id, path:path.join(' / '), parts:path, descendants });
      const key = prefix + '-' + ++sequence;
      const children = (byParent.get(id) ?? []).map((child,index) => {
        const markup = branch(child, path, index === 0);
        if (markup) for (const descendant of included.get(String(child.id)).descendants) descendants.add(descendant);
        return markup;
      }).join('');
      const choice = pick(id, item.name, path.join(' / '), ancestors.length && !children ? 'nav-team' : 'nav-projects');
      if (!children && !(loadChildren && item.hasChildren)) {
        return '<li class="tree-row tree-leaf"><span class="tree-toggle-space" aria-hidden="true"></span>' + choice + '</li>';
      }
      const controls = selectBranches ? toggle(key,item.name,open) + choice : toggle(key,item.name,open,'nav-projects');
      return '<li class="tree-branch"><div class="tree-row">' + controls + '</div>' +
        '<ul class="tree-children" id="' + key + '"' + (open ? '' : ' hidden') + '>' + children + '</ul></li>';
    }
    let branches = (byParent.get('') ?? []).map((item,index) => branch(item, [], !loadChildren && index === 0)).join('');
    for (const item of departments) branches += branch(item, [], false);
    host.innerHTML = '<div class="dev-tree" role="group" aria-label="组织结构"><div class="tree-surface tree-branch tree-root">' +
      '<div class="tree-tools" role="group" aria-label="分支显示">' +
      '<button class="tree-tool" type="button" data-tree-expand="true">展开全部</button>' +
      '<button class="tree-tool" type="button" data-tree-expand="false">收起全部</button></div>' +
      '<div class="tree-row tree-root-row">' +
      (includeAll ? toggle(rootKey,rootLabel,true) + pick('',rootLabel,rootLabel,'nav-components')
        : toggle(rootKey,rootLabel,true,'nav-components')) + '</div>' +
      '<ul class="tree-children" id="' + rootKey + '">' + branches + '</ul></div>' +
      '<p class="aux tree-selection" data-org-selection role="status"></p></div>';
    const pending = new Map();
    const loaded = new Set();
    async function expand(toggle) {
      const choice = toggle.parentElement.querySelector('[data-org]');
      const id = choice?.dataset.org;
      if (!id || loaded.has(id)) return;
      if (pending.has(id)) return pending.get(id);
      const list = document.getElementById(toggle.getAttribute('aria-controls'));
      const operation = loadChildren(id).then(items => {
        if (!host.contains(toggle)) return;
        const path = included.get(id).parts;
        list.innerHTML = items.map(child => branch(child,path,false)).join('');
        loaded.add(id);
      }).catch(error => {
        if (host.contains(toggle)) {
          setToggleClosed(toggle,list);
          onLoadError?.(error);
        }
      }).finally(() => pending.delete(id));
      pending.set(id,operation);
      return operation;
    }
    function setToggleClosed(toggle,list) {
      toggle.setAttribute('aria-expanded','false');
      toggle.title = '展开' + toggle.dataset.treeToggle;
      toggle.setAttribute('aria-label',toggle.title);
      list.hidden = true;
    }
    return mountTree(host.firstElementChild, { initialId, onChange, entries:included, expand:loadChildren && expand });
  }

  function mountTree(root, { initialId = '', onChange, entries, expand }) {
    const branches = () => [...root.querySelectorAll('[data-tree-toggle]')];
    const choices = () => [...root.querySelectorAll('[data-org]')];
    const status = root.querySelector('[data-org-selection]');
    const controls = ':scope > .tree-row > [data-tree-toggle]';
    const branchList = toggle => toggle.closest('.tree-branch').querySelector(':scope > .tree-children');
    let bulkRevision = 0;
    const rowControl = row => row.querySelector('[data-org]') ?? row.querySelector('[data-tree-toggle]');
    let selection;

    function setExpanded(toggle, expanded) {
      const label = (expanded ? '收起' : '展开') + toggle.dataset.treeToggle;
      toggle.setAttribute('aria-expanded',String(expanded));
      toggle.setAttribute('aria-label',label);
      toggle.title = label;
      branchList(toggle).hidden = !expanded;
      if (expanded) return expand?.(toggle);
    }

    function openBranch(toggle) {
      ++bulkRevision;
      const branch = toggle.closest('.tree-branch');
      for (const sibling of branch.parentElement.children) {
        if (sibling === branch || !sibling.matches('.tree-branch')) continue;
        setExpanded(sibling.querySelector(controls),false);
      }
      setExpanded(toggle,true);
    }

    function select(choice, notify = true) {
      selection = entries.get(choice.dataset.org);
      choices().forEach(button => button.setAttribute('aria-pressed',String(button === choice)));
      status.textContent = '已选择 ' + selection.path;
      if (notify) onChange?.(selection);
    }

    root.addEventListener('click',event => {
      const bulk = event.target.closest('[data-tree-expand]');
      if (bulk) {
        const revision = ++bulkRevision;
        const open = bulk.dataset.treeExpand === 'true';
        const seen = new Set();
        async function apply() {
          while (revision === bulkRevision && root.isConnected) {
            const next = branches().filter(toggle => !seen.has(toggle));
            if (!next.length) return;
            for (const toggle of next) {
              if (revision !== bulkRevision) return;
              seen.add(toggle);
              const loading = setExpanded(toggle,open);
              if (loading) await loading;
            }
          }
        }
        void apply();
        return;
      }
      const toggle = event.target.closest('[data-tree-toggle]');
      if (toggle) {
        ++bulkRevision;
        if (toggle.getAttribute('aria-expanded') === 'true') setExpanded(toggle,false);
        else openBranch(toggle);
        return;
      }
      const choice = event.target.closest('[data-org]');
      if (choice) select(choice);
    });

    root.addEventListener('keydown',event => {
      const row = event.target.closest('.tree-row');
      if (!row || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
      if (event.key === 'ArrowRight' || event.key === 'ArrowLeft') {
        event.preventDefault();
        const toggle = row.querySelector('[data-tree-toggle]');
        const expanded = toggle?.getAttribute('aria-expanded') === 'true';
        ++bulkRevision;
        if (event.key === 'ArrowRight' && toggle) {
          if (!expanded) openBranch(toggle);
          else {
            const first = branchList(toggle).querySelector('.tree-row');
            if (first) rowControl(first).focus();
          }
        }
        if (event.key === 'ArrowLeft') {
          if (expanded) setExpanded(toggle,false);
          else {
            const parent = row.closest('.tree-children')?.parentElement.querySelector(':scope > .tree-row');
            if (parent) rowControl(parent).focus();
          }
        }
        return;
      }
      if (!['ArrowUp','ArrowDown','Home','End'].includes(event.key)) return;
      event.preventDefault();
      const rows = [...root.querySelectorAll('.tree-row')].filter(item => item.getClientRects().length > 0);
      const index = rows.indexOf(row);
      const next = { ArrowUp:index - 1, ArrowDown:index + 1, Home:0, End:rows.length - 1 }[event.key];
      if (rows[next]) rowControl(rows[next]).focus();
    });

    const initialChoice = choices().find(button => button.dataset.org === initialId);
    if (initialChoice) {
      let branch = initialChoice.closest('.tree-branch');
      while (branch) {
        openBranch(branch.querySelector(controls));
        branch = branch.parentElement.closest('.tree-branch');
      }
      select(initialChoice,false);
    }
    return { value:() => selection, focus:() => (initialChoice ?? branches()[0]).focus() };
  }

  function combo(id, label, source, multiple = false) {
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

  function mountCombo(root, { choices, initial, preventEscape = true, signal } = {}) {
    const input = root.querySelector('[role="combobox"]');
    const output = root.querySelector('[data-combo-result]');
    const box = root.querySelector('.combo-box');
    const list = root.querySelector('[role="listbox"]');
    const empty = root.querySelector('.combo-empty');
    const values = root.querySelector('.combo-values');
    const multiple = root.dataset.multiple === 'true';
    const source = choices;
    const selected = new Set(initial ? [].concat(initial) : []);
    let active = -1;
    let matches = [];

    function highlight() {
      const options = [...list.children];
      options.forEach((option,index) => { option.dataset.active = String(index === active); });
      if (active < 0) input.removeAttribute('aria-activedescendant');
      else {
        input.setAttribute('aria-activedescendant',options[active].id);
        options[active].scrollIntoView({block:'nearest',inline:'nearest'});
      }
    }
    function show(query) {
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
    function pick(id) {
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
    list.addEventListener('pointerdown',event => { if(event.target.closest('[data-option]')) event.preventDefault(); });
    list.addEventListener('click',event => {
      const option = event.target.closest('[data-option]');
      if (option) pick(option.dataset.option);
    });
    values.addEventListener('click',event => {
      const button = event.target.closest('[data-remove]');
      if (!button) return;
      selected.delete(button.dataset.remove);
      input.focus();
      sync();
      if (!box.hidden) show(input.value);
    });
    root.addEventListener('focusout',event => {
      if (!root.contains(event.relatedTarget)) close();
    });
    root.closest('form')?.addEventListener('reset',() => {
      selected.clear();
      sync();
      close();
    }, { signal });
    if (initial) sync();
  }

  return { combo, mountCombo, escape, icon, fileSourceLocator, personAvatar, personButton, personView, mountPersonCards,
    openDialog, mountList, mountOrganizationTree, toast };
})();
