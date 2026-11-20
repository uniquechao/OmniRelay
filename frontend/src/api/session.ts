import { useSyncExternalStore } from 'react';
import { QueryClient } from '@tanstack/react-query';
import { client } from '../../../web/session-client.js';

let scope = client.scope;
let queries = new QueryClient();
function snapshot() { return { phase: client.phase, queries }; }
let current = snapshot();
client.addEventListener('change', () => {
  if (scope !== client.scope) {
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
