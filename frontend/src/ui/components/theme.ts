type Theme = 'light' | 'dark'
const root=document.documentElement
const storageKey='omni-relay.theme'
const url=new URL(location.href)
let saved:string|null=null
try {saved=localStorage.getItem(storageKey)} catch {}
const selected=[url.searchParams.get('theme'),saved].find(value=>value==='dark' || value==='light')
root.dataset.theme=selected ?? 'light'
  const icons = {
    light: '<svg class="icon theme-moon" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M20.7 14.2A9 9 0 0 1 9.8 3.3a9 9 0 1 0 10.9 10.9Z"/></svg>',
    dark: '<svg class="icon theme-sun" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M4.9 4.9l1.4 1.4m11.4 11.4 1.4 1.4m0-14.2-1.4 1.4M6.3 17.7l-1.4 1.4"/></svg>'
  };

export function syncThemeControls() {
  const theme:Theme=root.dataset.theme==='dark'?'dark':'light'
  for(const button of document.querySelectorAll<HTMLButtonElement>('[data-theme-toggle]')){
    button.innerHTML=icons[theme]
    button.title=theme==='dark'?'切换浅色主题':'切换深色主题'
    button.setAttribute('aria-label',button.title)
    button.setAttribute('aria-pressed',String(theme==='dark'))
  }
}
let transition:ViewTransition|undefined
let pending:Theme|undefined
  function clearReveal() {
    delete root.dataset.themeReveal;
    for (const name of ['--theme-reveal-x','--theme-reveal-y','--theme-reveal-end']) {
      root.style.removeProperty(name);
    }
  }

  function prepareReveal(control:HTMLElement) {
    if (!control.hasAttribute('data-theme-icon') || !window.CSS?.registerProperty) return;
    const { left, top, width, height } = control.getBoundingClientRect();
    const x = left + width / 2;
    const y = top + height / 2;
    const reach = Math.hypot(Math.max(x,innerWidth - x),Math.max(y,innerHeight - y));
    root.style.setProperty('--theme-reveal-x',x + 'px');
    root.style.setProperty('--theme-reveal-y',y + 'px');
    root.style.setProperty('--theme-reveal-end',Math.ceil(reach + 40) + 'px');
    root.dataset.themeReveal = '';
  }


export function toggleTheme(control:HTMLButtonElement){
  const theme:Theme=(pending ?? root.dataset.theme)==='dark'?'light':'dark'
  transition?.skipTransition()
  transition=undefined
  pending=undefined
  clearReveal()
  const apply=()=>{
    root.dataset.theme=theme
    try {localStorage.setItem(storageKey,theme)} catch {}
    const current=new URL(location.href)
    if(current.searchParams.has('theme')){
      current.searchParams.set('theme',theme)
      history.replaceState(history.state,'',current)
    }
    syncThemeControls()
    document.dispatchEvent(new Event('omni-relay:theme'))
  }
  if(matchMedia('(prefers-reduced-motion: reduce)').matches || !document.startViewTransition){apply();return}
  prepareReveal(control)
  pending=theme
  const next=document.startViewTransition(()=>{if(transition===next)apply()})
  transition=next
  const finish=()=>{if(transition===next){transition=undefined;pending=undefined;clearReveal()}}
  void next.finished.then(finish,finish)
}
