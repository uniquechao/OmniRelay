import { createRoot } from 'react-dom/client';
import { HashRouter, Routes, Route } from 'react-router';
import { QueryClientProvider } from '@tanstack/react-query';
import { Workbench } from '../features/workbench/Workbench';
import { client, useSession } from '../api/session';
import { Entry } from '../ui/Entry';

function App() {
  const { queries, phase } = useSession();
  if (phase !== 'active') return <Workbench />;
  return <QueryClientProvider client={queries}>
    <HashRouter><Routes>
      <Route index element={<Workbench />} />
      <Route path="/workbench" element={<Workbench />} />
      <Route path="*" element={<Entry title="预发布工作台"><p>此页面尚未迁移。</p></Entry>} />
    </Routes></HashRouter>
  </QueryClientProvider>;
}
client.watch(window, document);
void client.verify();
createRoot(document.getElementById('root')!).render(<App />);
