export function ProjectBot(props: {
  project: string;
  base: string;
  request: (path: string, options: {
    signal: AbortSignal;
    activity?: 'poll';
    method?: 'POST';
    body?: {text: string};
    idempotencyKey?: string;
  }) => Promise<unknown>;
  signal: AbortSignal;
}): import('react').ReactElement;
