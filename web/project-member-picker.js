export function memberPicker() {
  return `<div class="field member-picker">
    <label for="member-search">选择人员</label>
    <div class="dev-combo">
      <div class="member-search-control">
        <svg class="icon" aria-hidden="true"><use href="#chat-search-icon"/></svg>
        <input id="member-search" type="search" role="combobox" name="user_query" placeholder="搜索姓名或账号" autocomplete="off" maxlength="200" aria-autocomplete="list" aria-expanded="false" aria-controls="project-user-options">
        <span class="member-search-picked" role="status"></span>
      </div>
      <div class="combo-box" data-popup hidden>
        <div id="project-user-options" role="listbox" aria-label="可添加的人员"></div>
        <div class="member-search-status" role="status" hidden></div>
        <button class="button" type="button" data-retry hidden>重试</button>
        <div class="user-choice-pages" hidden><button type="button" data-previous>上一页</button><button type="button" data-more>下一页</button></div>
      </div>
    </div>
  </div>`;
}

export function mountMemberPicker(dialog, find, select) {
  const {escape, personAvatar} = window.OmniRelayUI;
  const box = dialog.querySelector('.member-picker');
  const input = box.querySelector('input'), popup = box.querySelector('[data-popup]');
  const list = box.querySelector('[role=listbox]'), status = box.querySelector('[role=status]');
  const pages = box.querySelector('.user-choice-pages'), retry = box.querySelector('[data-retry]');
  let page = 1, revision = 0, active = -1, items = [], chosen, timer;

  function highlight(index) {
    list.children[active]?.removeAttribute('data-active');
    active = index;
    const row = list.children[index];
    if (!row) { input.removeAttribute('aria-activedescendant'); return; }
    row.dataset.active = 'true';
    input.setAttribute('aria-activedescendant', row.id);
    row.scrollIntoView({block:'nearest'});
  }
  function close() {
    revision++;
    clearTimeout(timer);
    popup.hidden = true;
    input.setAttribute('aria-expanded','false');
    highlight(-1);
  }
  function search(next = 1, delay = 0, initial) {
    const ticket = ++revision;
    clearTimeout(timer);
    page = next;
    items = [];
    highlight(-1);
    list.replaceChildren();
    pages.hidden = retry.hidden = true;
    popup.hidden = status.hidden = false;
    status.textContent = '正在搜索…';
    input.setAttribute('aria-expanded','true');
    const query = chosen ? '' : input.value.trim();
    timer = setTimeout(async () => {
      try {
        const result = await find(query, next);
        if (ticket !== revision || !dialog.open) return;
        items = result;
        list.innerHTML = result.map((user,i) => {
          const detail = user.username && user.username !== user.display_name ? '<small>'+escape(user.username)+'</small>' : '';
          return `<div role="option" class="combo-option member-option" id="member-option-${i}" data-user-choice="${i}" aria-selected="${chosen?.account_id===user.account_id}">
            ${personAvatar(user.display_name,user.account_id)}<span>${escape(user.display_name)}</span>${detail}</div>`;
        }).join('');
        list.scrollTop = 0;
        status.hidden = result.length > 0;
        status.textContent = result.length ? '' : '没有可添加的匹配人员';
        pages.hidden = next === 1 && result.length < 25;
        pages.querySelector('[data-previous]').disabled = next === 1;
        pages.querySelector('[data-more]').disabled = result.length < 25;
        if (initial && result.length) highlight(initial === 'last' ? result.length - 1 : 0);
      } catch {
        if (ticket !== revision || !dialog.open) return;
        status.textContent = '人员列表加载失败';
        retry.hidden = false;
      }
    }, delay);
  }
  function pick(index) {
    chosen = items[index];
    if (!chosen) return;
    select(chosen);
    input.value = chosen.display_name;
    box.querySelector('.member-search-picked').textContent = '已选';
    input.focus();
    close();
    input.dispatchEvent(new Event('change',{bubbles:true}));
  }
  input.onclick = () => { if (popup.hidden) search(); };
  input.oninput = () => {
    chosen = null;
    select(null);
    box.querySelector('.member-search-picked').textContent = '';
    search(1,180);
  };
  input.onkeydown = event => {
    if (event.isComposing) return;
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      const down = event.key === 'ArrowDown';
      if (popup.hidden) search(1,0,down ? 'first' : 'last');
      else if (items.length) highlight(down ? (active+1)%items.length : (active<=0 ? items.length-1 : active-1));
    } else if (event.key === 'Enter') {
      event.preventDefault();
      if (popup.hidden) search();
      else if (items.length) pick(active < 0 ? 0 : active);
    }
  };
  box.addEventListener('keydown', event => {
    if (event.key === 'Escape' && !popup.hidden && !event.isComposing) {
      event.preventDefault(); event.stopPropagation(); close(); input.focus();
    }
  });
  list.onmousedown = event => event.preventDefault();
  list.onclick = event => {
    const row = event.target.closest('[data-user-choice]');
    if (row) pick(Number(row.dataset.userChoice));
  };
  pages.querySelector('[data-previous]').onclick = () => {input.focus(); search(page-1);};
  pages.querySelector('[data-more]').onclick = () => {input.focus(); search(page+1);};
  retry.onclick = () => {input.focus(); search(page);};
  box.addEventListener('focusout', event => { if (!box.contains(event.relatedTarget)) close(); });
  dialog.addEventListener('pointerdown', event => { if (!box.contains(event.target)) close(); });
  dialog.addEventListener('close', close, {once:true});
}
