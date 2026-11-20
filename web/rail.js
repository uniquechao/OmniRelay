export function mountRail(shell, groups, signal) {
  const railToggle = shell.querySelector('.rail-toggle');
  const railResizer = shell.querySelector('.rail-resizer');
  const compactRail = matchMedia('(max-width: 720px)');
  let railPreference;

  try { railPreference = localStorage.getItem('omni-relay.rail'); } catch {}
  function renderRail() {
    const collapsed = railPreference === 'collapsed' || (!railPreference && compactRail.matches);
    shell.dataset.rail = collapsed ? 'collapsed' : 'expanded';
    const label = collapsed ? '展开侧栏' : '收起侧栏';
    railToggle.setAttribute('aria-expanded', String(!collapsed));
    railToggle.setAttribute('aria-label', label);
    railToggle.title = label;
    railToggle.querySelector('span').textContent = label;
    railResizer.setAttribute('aria-valuenow', collapsed ? '52' : '166');
    railResizer.setAttribute('aria-valuetext', collapsed ? '已收起，52 像素' : '已展开，166 像素');
  }
  function setRail(state) {
    railPreference = state;
    try { localStorage.setItem('omni-relay.rail', railPreference); } catch {}
    renderRail();
  }
  railToggle.addEventListener('click', () => {
    setRail(shell.dataset.rail === 'collapsed' ? 'expanded' : 'collapsed');
  }, { signal });
  for (const group of groups) {
    group.querySelector('summary').addEventListener('click', event => {
      if (shell.dataset.rail !== 'collapsed') return;
      event.preventDefault();
      setRail('expanded');
      group.open = true;
    }, { signal });
  }
  compactRail.addEventListener('change', renderRail, { signal });
  renderRail();

  let railDrag = null;
  function finishRailDrag(commit) {
    if (!railDrag) return;
    const { id, delta, state } = railDrag;
    railDrag = null;
    delete shell.dataset.dragging;
    shell.style.removeProperty('--rail-drag-width');
    if (railResizer.hasPointerCapture(id)) railResizer.releasePointerCapture(id);
    if (commit && Math.abs(delta) >= 24) setRail(delta > 0 ? 'expanded' : 'collapsed');
    else shell.dataset.rail = state;
  }
  railResizer.addEventListener('pointerdown', event => {
    if (event.button !== 0 || railDrag) return;
    const state = shell.dataset.rail;
    railDrag = { id:event.pointerId, start:event.clientX, delta:0, state, width:state === 'collapsed' ? 52 : 166 };
    railResizer.focus({preventScroll:true});
    railResizer.setPointerCapture(event.pointerId);
    shell.dataset.dragging = '';
  }, { signal });
  window.addEventListener('pointermove', event => {
    if (railDrag?.id !== event.pointerId) return;
    railDrag.delta = event.clientX - railDrag.start;
    const width = Math.max(52,Math.min(166,railDrag.width + railDrag.delta));
    shell.style.setProperty('--rail-drag-width',width + 'px');
    shell.dataset.rail = width >= 124 ? 'expanded' : 'collapsed';
  }, { signal });
  window.addEventListener('pointerup', event => {
    if (railDrag?.id !== event.pointerId) return;
    railDrag.delta = event.clientX - railDrag.start;
    finishRailDrag(true);
  }, { signal });
  window.addEventListener('pointercancel', event => {
    if (railDrag?.id === event.pointerId) finishRailDrag(false);
  }, { signal });
  railResizer.addEventListener('lostpointercapture', () => finishRailDrag(false), { signal });
  railResizer.addEventListener('keydown', event => {
    if (event.key === 'Escape' && railDrag) { finishRailDrag(false); return; }
    const keys = { ArrowLeft:'collapsed', Home:'collapsed', ArrowRight:'expanded', End:'expanded',
      Enter:shell.dataset.rail === 'collapsed' ? 'expanded' : 'collapsed' };
    if (Object.hasOwn(keys,event.key)) { event.preventDefault(); setRail(keys[event.key]); }
  }, { signal });
}
