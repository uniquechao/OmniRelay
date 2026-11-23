export function connectSplitPane(container:HTMLElement, {storageKey,defaultRatio,minDirectory,signal}:{storageKey:string;defaultRatio:number;minDirectory:number;signal:AbortSignal}) {
  let width:number|undefined;
  const handle = container.querySelector<HTMLElement>('.split-handle');
  if (!handle) return;
  if (storageKey) {
    try {
      const stored=localStorage.getItem(storageKey)
      if(stored!==null && Number.isFinite(Number(stored))) width=Number(stored);
    } catch {
      // Use the default width when the browser cannot read the preference.
    }
  }
  let preferred = Number.isFinite(width) ? width : undefined;
  let current=0;
  let min=0;
  let max=0;
  let drag:{id:number;x:number;width:number}|undefined;
  const listen = {signal};

  const apply=(value:number)=>{
    current = Math.round(Math.min(max, Math.max(min, value)));
    container.style.setProperty('--split-width', current + 'px');
    handle.setAttribute('aria-valuenow', String(current));
    handle.setAttribute('aria-valuetext', current + ' 像素');
  }

  const refresh=()=>{
    const total = container.clientWidth;
    if (!total) return;
    const gap = handle.offsetWidth;
    const mainMinimum = Math.min(284, Math.ceil(total * .61));
    max = Math.max(0, Math.min(500, total - gap - mainMinimum));
    min = Math.min(150, max);
    handle.setAttribute('aria-valuemin', String(min));
    handle.setAttribute('aria-valuemax', String(max));
    if (!gap && drag) endDrag(false);
    const enabled = gap > 0 && min < max && !container.hasAttribute('data-collapsed');
    handle.setAttribute('aria-disabled', String(!enabled));
    handle.tabIndex = enabled ? 0 : -1;
    apply(drag ? current : preferred ?? Math.max(minDirectory, Math.min(350, (total - gap) * defaultRatio)));
  }

  function saveWidth() {
    preferred = current;
    if (storageKey) {
      try {
        localStorage.setItem(storageKey, String(preferred));
      } catch {
        // Keep the preference for the current page.
      }
    }
  }

  const endDrag=(keepWidth:boolean)=>{
    if(!drag)return;
    const {id, width: initial} = drag;
    drag = undefined;
    container.removeAttribute('data-split-dragging');
    if (handle.hasPointerCapture(id)) handle.releasePointerCapture(id);
    if (keepWidth && current !== initial) saveWidth();
    else refresh();
  }

  handle.addEventListener('pointerdown', event => {
    if (event.button !== 0 || !event.isPrimary || handle.tabIndex < 0 || drag) return;
    event.preventDefault();
    const actualWidth = Math.round(container.firstElementChild?.getBoundingClientRect().width ?? 0);
    drag = {id:event.pointerId, x:event.clientX, width:actualWidth};
    container.setAttribute('data-split-dragging', '');
    apply(actualWidth);
    handle.focus({preventScroll:true});
    handle.setPointerCapture(event.pointerId);
  }, listen);

  handle.addEventListener('pointermove', event => {
    if (event.pointerId === drag?.id) apply(drag.width + event.clientX - drag.x);
  }, listen);

  handle.addEventListener('pointerup', event => {
    if (event.pointerId !== drag?.id) return;
    apply(drag.width + event.clientX - drag.x);
    endDrag(true);
  }, listen);

  for (const type of ['pointercancel', 'lostpointercapture'] as const) {
    handle.addEventListener(type, event => {
      if (event.pointerId === drag?.id) endDrag(false);
    }, listen);
  }

  handle.addEventListener('keydown', event => {
    if (drag) {
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        endDrag(false);
      }
      return;
    }
    if (handle.tabIndex < 0) return;
    const targets:Record<string,number> = {ArrowLeft:current - 16, ArrowRight:current + 16, Home:min, End:max};
    if (!Object.hasOwn(targets, event.key)) return;
    event.preventDefault();
    const previous = current;
    apply(targets[event.key]);
    if (current !== previous) saveWidth();
  }, listen);

  refresh();
  const observer = new ResizeObserver(() => {
    if (container.isConnected) refresh();
    else observer.disconnect();
  });
  observer.observe(container);
  signal?.addEventListener('abort', () => {
    if (drag) endDrag(false);
    observer.disconnect();
  }, {once: true});
}
