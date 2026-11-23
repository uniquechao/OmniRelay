import {closestControl,requiredElement} from './dom'
import {escape,icon} from './controls'
import type {Department,Selection} from './components/runtime'
type Entry=Selection & {parts?:string[];descendants?:Set<string>}
type TreeOptions={initialId?:string;includeAll?:boolean;selectBranches?:boolean;onChange?:(entry:Entry)=>void;
 loadChildren?:(id:string)=>Promise<Department[]>;onLoadError?:(error:unknown)=>void}
let treeNumber=0
export function mountOrganizationTree(host:HTMLElement,departments:Department[], {
  initialId = '', onChange, includeAll = true, selectBranches = true, loadChildren, onLoadError
}:TreeOptions = {}) {
  const byParent = new Map<string,Department[]>();
  const departmentIds = new Set(departments.map(item => String(item.id)));
  const rendered = new Set();
  const included = new Map<string,Entry>();
  let sequence = 0;
  const prefix = 'organization-tree-' + ++treeNumber;
  const rootKey = prefix + '-root';
  const rootLabel = includeAll ? '全部组织' : 'OmniRelay';
  if (includeAll) included.set('', { id:'', path:rootLabel });
  for (const item of departments) {
    const parent = departmentIds.has(String(item.parentId)) ? String(item.parentId) : '';
    if (!byParent.has(parent)) byParent.set(parent, []);
    byParent.get(parent)!.push(item);
  }

  const pick = (id:string,label:string,path:string,symbol:string) =>
    '<button class="tree-choice" type="button" data-org="' + escape(id) + '" aria-pressed="false"' +
    ' title="' + escape(path) + '">' + icon(symbol) + '<span class="tree-node-label">' + escape(label) + '</span>' +
    '<span data-chosen aria-hidden="true">✓</span></button>';
  const toggle = (key:string,label:string,open:boolean,symbol?:string) => {
    const action = (open ? '收起' : '展开') + label;
    return '<button class="tree-toggle' + (symbol ? ' tree-branch-label' : '') + '" type="button" data-tree-toggle="' +
      escape(label) + '" aria-label="' + escape(action) + '" title="' + escape(action) + '" aria-controls="' + key +
      '" aria-expanded="' + open + '">' + icon('action-expand','icon tree-expander') +
      (symbol ? icon(symbol) + '<span class="tree-node-label">' + escape(label) + '</span>' : '') + '</button>';
  };
  function branch(item:Department,ancestors:string[],open:boolean):string {
    const id = String(item.id);
    if (rendered.has(id)) return '';
    rendered.add(id);
    const path = [...ancestors, item.name];
    const descendants = new Set([id]);
    included.set(id, { id, path:path.join(' / '), parts:path, descendants });
    const key = prefix + '-' + ++sequence;
    const children = (byParent.get(id) ?? []).map((child,index) => {
      const markup = branch(child, path, index === 0);
      if (markup) for (const descendant of included.get(String(child.id))?.descendants ?? []) descendants.add(descendant);
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
  const pending = new Map<string,Promise<void>>();
  const loaded = new Set();
  async function expand(toggle:HTMLElement):Promise<void> {
    const choice = toggle.parentElement?.querySelector<HTMLElement>('[data-org]');
    const id = choice?.dataset.org;
    if (!id || loaded.has(id)) return;
    if (pending.has(id)) return pending.get(id);
    const list = document.getElementById(toggle.getAttribute('aria-controls') ?? '');
    if(!list || !loadChildren)return;
    const operation = loadChildren(id).then(items => {
      if (!host.contains(toggle)) return;
      const path = included.get(id)?.parts ?? [];
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
  function setToggleClosed(toggle:HTMLElement,list:HTMLElement) {
    toggle.setAttribute('aria-expanded','false');
    toggle.title = '展开' + toggle.dataset.treeToggle;
    toggle.setAttribute('aria-label',toggle.title);
    list.hidden = true;
  }
  return mountTree(requiredElement(host,'.dev-tree',HTMLElement), { initialId, onChange, entries:included, expand:loadChildren && expand });
}

function mountTree(root:HTMLElement,{initialId='',onChange,entries,expand}:{initialId?:string;onChange?:TreeOptions['onChange'];entries:Map<string,Entry>;expand?:(node:HTMLElement)=>Promise<void>}) {
  const branches = () => [...root.querySelectorAll<HTMLElement>('[data-tree-toggle]')];
  const choices = () => [...root.querySelectorAll<HTMLElement>('[data-org]')];
  const status = requiredElement(root,'[data-org-selection]',HTMLElement);
  const controls = ':scope > .tree-row > [data-tree-toggle]';
  const branchList = (toggle:HTMLElement) => requiredElement(toggle.closest('.tree-branch') ?? root,':scope > .tree-children',HTMLElement);
  let bulkRevision = 0;
  const rowControl = (row:Element) => row.querySelector<HTMLElement>('[data-org]') ?? row.querySelector<HTMLElement>('[data-tree-toggle]');
  let selection:Entry|undefined;

  function setExpanded(toggle:HTMLElement|null, expanded:boolean) {
    if (!toggle) return;
    const label = (expanded ? '收起' : '展开') + toggle.dataset.treeToggle;
    toggle.setAttribute('aria-expanded',String(expanded));
    toggle.setAttribute('aria-label',label);
    toggle.title = label;
    branchList(toggle).hidden = !expanded;
    if (expanded) return expand?.(toggle);
  }

  function openBranch(toggle:HTMLElement|null) {
    ++bulkRevision;
    const branch = toggle?.closest('.tree-branch');
    if (!branch?.parentElement || !toggle) return;
    for (const sibling of branch.parentElement.children) {
      if (sibling === branch || !sibling.matches('.tree-branch')) continue;
      setExpanded(sibling.querySelector(controls),false);
    }
    setExpanded(toggle,true);
  }

  function select(choice:HTMLElement, notify = true) {
    selection = entries.get(choice.dataset.org ?? '');
    choices().forEach(button => button.setAttribute('aria-pressed',String(button === choice)));
    status.textContent = '已选择 ' + selection?.path;
    if (notify) selection && onChange?.(selection);
  }

  root.addEventListener('click',event => {
    const bulk = closestControl(event,'[data-tree-expand]');
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
    const toggle = closestControl(event,'[data-tree-toggle]');
    if (toggle) {
      ++bulkRevision;
      if (toggle.getAttribute('aria-expanded') === 'true') setExpanded(toggle,false);
      else openBranch(toggle);
      return;
    }
    const choice = closestControl(event,'[data-org]');
    if (choice) select(choice);
  });

  root.addEventListener('keydown',event => {
    const row = closestControl(event,'.tree-row');
    if (!row || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    if (event.key === 'ArrowRight' || event.key === 'ArrowLeft') {
      event.preventDefault();
      const toggle = row.querySelector<HTMLElement>('[data-tree-toggle]');
      const expanded = toggle?.getAttribute('aria-expanded') === 'true';
      ++bulkRevision;
      if (event.key === 'ArrowRight' && toggle) {
        if (!expanded) openBranch(toggle);
        else {
          const first = branchList(toggle).querySelector('.tree-row');
          if (first) rowControl(first)?.focus();
        }
      }
      if (event.key === 'ArrowLeft') {
        if (expanded) setExpanded(toggle,false);
        else {
          const parent = row.closest('.tree-children')?.parentElement?.querySelector(':scope > .tree-row');
          if (parent) rowControl(parent)?.focus();
        }
      }
      return;
    }
    if (!['ArrowUp','ArrowDown','Home','End'].includes(event.key)) return;
    event.preventDefault();
    const rows = [...root.querySelectorAll<HTMLElement>('.tree-row')].filter(item => item.getClientRects().length > 0);
    const index = rows.indexOf(row);
    const next = { ArrowUp:index - 1, ArrowDown:index + 1, Home:0, End:rows.length - 1 }[event.key];
    if (next!==undefined && rows[next]) rowControl(rows[next])?.focus();
  });

  const initialChoice = choices().find(button => button.dataset.org === initialId);
  if (initialChoice) {
    let branch = initialChoice.closest('.tree-branch');
    while (branch) {
      openBranch(branch.querySelector(controls));
      branch = branch.parentElement?.closest('.tree-branch') ?? null;
    }
    select(initialChoice,false);
  }
  return { value:() => selection, focus:() => (initialChoice ?? branches()[0]).focus() };
}
