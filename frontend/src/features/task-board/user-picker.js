const escape = value => window.OmniRelayUI.escape(value ?? '');

export function userSearchText(user) {
  return [user.display_name, user.name, user.username, user.account_id, user.id].filter(Boolean).join(' ');
}

// Group candidates can be selected before their first workbench login.
export function userChoice(user, value, attribute = 'data-user-choice') {
  const name = user.display_name || user.name || user.account_id || user.id;
  const account = user.username || (user.account_id?.startsWith('feishu:') ? '群成员' : user.account_id || user.id);
  return `<button class="button user-choice" type="button" ${attribute}="${escape(value)}">
    ${window.OmniRelayUI.personAvatar(name, user.account_id || user.id || name)}
    <span class="user-choice-copy"><strong>${escape(name)}</strong><small>${escape(account)}</small></span>
  </button>`;
}

export function navigateUserChoices(event, box) {
  const input = box.querySelector('input[type=search]');
  const list = box.querySelector('[data-results]');
  if (!input || !list || list.hidden) return;
  const buttons = [...list.querySelectorAll('button:not(:disabled)')];
  const index = buttons.indexOf(event.target);
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopPropagation();
    list.hidden = true; input.focus(); list.hidden = true;
  } else if (event.key === 'ArrowDown' && buttons.length) {
    event.preventDefault(); buttons[(index + 1) % buttons.length].focus();
  } else if (event.key === 'ArrowUp' && buttons.length) {
    event.preventDefault();
    if (index <= 0) input.focus();
    else buttons[index - 1].focus();
  } else if (event.key === 'Enter' && event.target === input) {
    event.preventDefault(); buttons[0]?.focus();
  }
}
