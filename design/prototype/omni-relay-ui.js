/* Shared list page and toast behaviors for the OmniRelay workbench prototype. */
window.OmniRelayUI = (() => {
  const listStates = new Map();
  const collator = new Intl.Collator('zh-CN', { numeric: true });
  const pageSizes = [10, 20, 50];
  const selectionModes = { none: '不选择', single: '单选', multiple: '多选' };
  const tipLevels = { info: '信息', success: '成功', warning: '警告', error: '错误' };
  const tipTimers = new WeakMap();
  let dialogNumber = 0;

  function escape(value) {
    return String(value).replace(/[&<>"']/g, char => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    })[char]);
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
    let hasGuard = false;
    let lastField;
    dialog.querySelector('.dialog-body').addEventListener('focusin',event => {
      if (event.target.matches('input, select, textarea')) lastField = event.target;
    });
    function requestClose() {
      if (!isDirty?.(form)) { dialog.close('cancel'); return; }
      if (hasGuard) return;
      hasGuard = true;
      openDialog({
        title:'保存修改？', variant:'confirm', tone:'warning',
        body:'<p class="dialog-note">有未保存的修改，直接关闭会丢失本次更改。</p>',
        cancelLabel:'继续编辑', discard:'不保存', confirm:'保存并关闭',
        onClose(confirmed, result) {
          hasGuard = false;
          if (confirmed) form.requestSubmit();
          else if (result === 'discard') dialog.close('cancel');
          else lastField?.focus();
        }
      });
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
    form.addEventListener('submit',event => {
      event.preventDefault();
      if (!confirm && !onSubmit) return;
      clearError();
      const result = onSubmit?.(form,event.submitter);
      if (result?.keepOpen) return;
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
    if (!listStates.has(config.key)) {
      listStates.set(config.key, {
        query: '', filters: {}, page: 0, pageSize: pageSizes[0], selected: new Set(),
        sort: config.defaultSort ?? null, mode: config.selection === false ? 'none' : 'multiple', columnWidths: null
      });
    }
    return listStates.get(config.key);
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
        ${filters.map(filter => '<label>' + filter.label + '<select class="select" data-filter="' + filter.key + '">' +
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
        filters.every(filter => !state.filters[filter.key] || String(row[filter.key]) === state.filters[filter.key])
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
      const activeFilters = filters.filter(filter => state.filters[filter.key]);
      find('[data-filter-summary]').textContent = activeFilters.map(filter => filter.label + '：' + filter.options[state.filters[filter.key]]).join(' · ');
      find('[data-context]').hidden = !(count || activeFilters.length || config.action);
    }

    function paint() {
      const rows = filteredRows();
      const existingIds = new Set(config.rows.map(row => row.id));
      for (const id of state.selected) {
        if (!existingIds.has(id)) state.selected.delete(id);
      }
      const pageCount = Math.ceil(rows.length / state.pageSize);
      state.page = Math.max(0, Math.min(state.page, pageCount - 1));
      visible = rows.slice(state.page * state.pageSize, (state.page + 1) * state.pageSize);
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
      empty.textContent = config.rows.length ? '没有匹配的' + config.itemLabel + '。' : '暂无' + config.itemLabel + '。';
      find('[data-total]').textContent = '共 ' + rows.length + ' 条';
      find('[data-page]').textContent = '第 ' + (pageCount ? state.page + 1 : 0) + ' / ' + pageCount + ' 页';
      find('[data-prev]').disabled = state.page === 0;
      find('[data-next]').disabled = state.page + 1 >= pageCount;
      scroller.scrollTop = 0;
      updateSelection();
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
      const options = { signal: listeners.signal };
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
    });
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
    });
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
        state.filters = {};
        state.page = 0;
        state.selected.clear();
        find('[data-search]').value = '';
        root.querySelectorAll('[data-filter]').forEach(field => { field.value = ''; });
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
    });

    applyColumnWidths();
    paint();
    return { refresh: paint, resetPage() { state.page = 0; paint(); } };
  }

  function mountAccount(onLogout) {
    const menu = document.getElementById('account-menu');
    const shell = document.querySelector('.shell');
    const getTrigger = () => shell.querySelector('.rail').getBoundingClientRect().width
      ? shell.querySelector('.rail .account-trigger') : shell.querySelector('.mobile-account');
    let trigger;
    const details = '<dl class="facts"><div><dt>用户名</dt><dd>苏涵</dd></div>' +
      '<div><dt>角色</dt><dd>系统管理员</dd></div><div><dt>身份来源</dt><dd>飞书 · 示例账号</dd></div></dl>';
    const dialogs = {
      profile: { title:'个人信息', body:'<div class="dialog-record">' + details + '</div>' },
      password: {
        title:'修改密码', body:'<p class="dialog-note">此账号由飞书管理，请在飞书的账号安全设置中修改密码。</p>'
      },
      logout: {
        title:'退出工作台', variant:'confirm', body:'<p class="dialog-note">退出后将返回飞书登录页。</p>', confirm:'退出',
        onClose(confirmed) {
          if (!confirmed) return;
          onLogout();
        }
      }
    };
    function placeMenu() {
      const anchor = getTrigger().getBoundingClientRect();
      const left = Math.max(8, Math.min(anchor.left, innerWidth - menu.offsetWidth - 8));
      const top = anchor.top - menu.offsetHeight - 6;
      menu.style.inset = (top >= 8 ? top : anchor.bottom + 6) + 'px auto auto ' + left + 'px';
    }
    menu.addEventListener('beforetoggle', event => {
      if (event.newState === 'open') {
        trigger = getTrigger();
        requestAnimationFrame(placeMenu);
      }
    });
    addEventListener('resize', () => { if (menu.matches(':popover-open')) placeMenu(); });
    menu.addEventListener('click', event => {
      const option = event.target.closest('[data-account]');
      if (!option) return;
      menu.hidePopover();
      openDialog({
        ...dialogs[option.dataset.account],
        restoreFocus:() => shell.hidden ? null : trigger?.getClientRects().length ? trigger : getTrigger()
      });
    });
  }

  return { escape, icon, openDialog, mountList, toast, mountAccount };
})();
