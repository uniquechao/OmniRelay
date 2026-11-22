// Boundary between the JS workbench route and the separately loaded DSH client.
export function mountProjectBot(
  host: HTMLElement,
  project: string,
  base: string,
  request: (path: string, options: {
    signal: AbortSignal;
    activity?: 'poll';
    method?: 'POST';
    body?: {text: string};
    idempotencyKey?: string;
  }) => Promise<unknown>,
  signal: AbortSignal,
): void;
