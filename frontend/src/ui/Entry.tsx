import type { ReactNode } from 'react';
import logo from '../../../design/prototype/omni-relay-logo-bicolor.png';
import '../../../design/prototype/omni-relay-colors.css';
import './entry.css';

export function Entry({ title, children }: { title: string; children: ReactNode }) {
  return <main>
    <img src={logo} width="195" height="65" alt="OmniRelay" />
    <h1>{title}</h1>
    {children}
  </main>;
}
