import '../../../../design/prototype/omni-relay-ui.js';
import '../../../../design/prototype/omni-relay-loading.js';
import '../../../../design/prototype/omni-relay-theme.js';
import '../../../../design/prototype/omni-relay-login-motion.js';
import '../../../../design/prototype/omni-relay-colors.css';
import '../workbench.css';
import '../../../../design/prototype/omni-relay-ui.css';
import '../../../../design/prototype/omni-relay-loading.css';
import '../../../../design/prototype/omni-relay-interactions.css';
import '../../../../design/prototype/omni-relay-system.css';
import {useLayoutEffect, useRef} from 'react';

export type Choice = { id: string; label: string; detail?: string; search?: string };
export type Department = { id: string; name: string; parentId?: string };
export type Selection = { id: string; path: string };
export type Person = { name: string; personKey: string; org?: string; source?: string };
export type Tone = 'info' | 'success' | 'warning' | 'error';
export type Wait = {size?: 'page' | 'compact' | 'inline'; label?: string; progress?: number | null};
export type DialogOptions = {
  title: string; description?: string; body: string; drawer?: 'normal' | 'wide';
  variant?: 'form' | 'detail' | 'confirm'; confirm?: string; tone?: 'default' | 'info' | 'danger' | 'warning';
  isDirty?: (form: HTMLFormElement) => boolean;
  onSubmit?: (form: HTMLFormElement) => void | {field: HTMLElement; message: string} | Promise<void | {field: HTMLElement; message: string}>;
  cancelLabel?: string; discard?: string; onClose?: (confirmed: boolean, result?: string) => void; restoreFocus?: () => HTMLElement | null;
};
declare global {
  interface Window {
    OmniRelayTheme: {syncControls(): void};
    OmniRelayUI: {
      escape(value: unknown): string;
      toast(tone: Tone, message: string): void;
      openDialog(options: DialogOptions): HTMLDialogElement;
      combo(id: string, label: string, source: string, multiple?: boolean): string;
      mountCombo(root: Element, options: {choices: Choice[]; initial?: string[]; signal: AbortSignal}): void;
      mountOrganizationTree(host: HTMLElement, nodes: Department[], options: {initialId?:string; includeAll?: boolean; selectBranches?:boolean; onChange?: (value: Selection) => void}): {focus(): void; value(): Selection | undefined};
      personView(person: Person): string;
      mountPersonCards(root: HTMLElement, options: {signal: AbortSignal}): void;
      mountList(root: HTMLElement, config: object): {refresh(): void};
    };
    OmniRelayLoading: {markup(options: Wait): string};
    OmniRelayLoginMotion(stage: HTMLElement): (() => void) | undefined;
  }
}
export const ui = window.OmniRelayUI;
// 原型控件独占一个子树；每次挂载均有独立的节点和取消信号。
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
