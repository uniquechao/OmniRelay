import * as ui from '../widgets';
export {ui};
import '../../../../design/prototype/omni-relay-colors.css';
import '../workbench.css';
import '../../../../design/prototype/omni-relay-ui.css';
import '../../../../design/prototype/omni-relay-loading.css';
import '../../../../design/prototype/omni-relay-interactions.css';
import '../../../../design/prototype/omni-relay-system.css';
import {useLayoutEffect, useRef} from 'react';

export type Choice = { id: string; label: string; detail?: string; search?: string };
export type Department = { id: string; name: string; parentId?: string; hasChildren?:boolean };
export type Selection = { id: string; path: string };
export type Person = {person_key?:string;organization_paths?:string[][];resigned?:boolean; name: string; personKey?: string; org?: string; source?: string };
export type Tone = 'info' | 'success' | 'warning' | 'error';
export type Wait = {size?: 'page' | 'compact' | 'inline'; label?: string; progress?: number | null};
type DialogResult=void | {field:HTMLElement;message:string} | {keepOpen:true};
export type DialogOptions = {
  title: string; description?: string; body: string; drawer?: 'normal' | 'wide';
  variant?: 'form' | 'detail' | 'confirm'; confirm?: string; tone?: 'default' | 'info' | 'danger' | 'warning';
  isDirty?: (form: HTMLFormElement) => boolean;
  onSubmit?: (form: HTMLFormElement, submitter:HTMLElement|null) => DialogResult | Promise<DialogResult>;
  cancelLabel?: string; discard?: string; onClose?: (confirmed: boolean, result?: string) => void; restoreFocus?: (confirmed?:boolean) => HTMLElement | null;
};
export function useWidget(mount: (host: HTMLDivElement, signal: AbortSignal) => void | (() => void), deps: React.DependencyList) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const host = document.createElement('div');
    ref.current!.append(host);
    const lifetime = new AbortController();
    const dispose = mount(host, lifetime.signal);
    return () => { lifetime.abort(); dispose?.(); host.remove(); };
  }, deps);
  return ref;
}
export function useLatest<T>(value: T) {
  const ref = useRef(value);
  useLayoutEffect(() => { ref.current = value; }, [value]);
  return ref;
}
