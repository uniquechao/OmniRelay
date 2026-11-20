import { createRoot } from 'react-dom/client';
import { HashRouter, Routes, Route } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Workbench } from '../features/workbench/Workbench';
import { Entry } from '../ui/Entry';

createRoot(document.getElementById('root')!).render(
  <QueryClientProvider client={new QueryClient()}>
    <HashRouter><Routes>
      <Route index element={<Workbench />} />
      <Route path="/workbench" element={<Workbench />} />
      <Route path="*" element={<Entry title="预发布工作台"><p>此页面尚未迁移。</p></Entry>} />
    </Routes></HashRouter>
  </QueryClientProvider>
);
