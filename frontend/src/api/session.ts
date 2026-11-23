import { useSyncExternalStore } from 'react';
import { QueryClient } from '@tanstack/react-query';
import { client } from './session-client.js';

let scope = client.scope;
let queries = new QueryClient();
let revision=0;
function snapshot() { return { phase: client.phase, queries, revision }; }
let current = snapshot();
client.addEventListener('change', event => {
  if (scope !== client.scope) {
    revision++;
    for(const overlay of document.querySelectorAll<HTMLDialogElement>('dialog[open]')) overlay.close('cancel');
    if ((event as CustomEvent).detail === 'identity-changed') history.replaceState(null,'','/');
    queries.clear();
    scope = client.scope;
    queries = new QueryClient();
  } else if (client.phase === 'unverified') {
    void queries.cancelQueries();
  }
  current = snapshot();
});

function subscribe(notify: () => void) {
  client.addEventListener('change', notify);
  return () => client.removeEventListener('change', notify);
}
export function useSession() {
  return useSyncExternalStore(subscribe, () => current);
}
export { client };
