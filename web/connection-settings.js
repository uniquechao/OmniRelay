export function mountConnectionSettings(page, readConnections, signal) {
  page.innerHTML = `
    <header class="head"><h1 id="page-heading" tabindex="-1">连接设置</h1></header>
    <section class="panel" aria-labelledby="connections-heading">
      <header class="section-head"><h2 id="connections-heading">服务连接</h2></header>
      <div data-connections-loading></div>
      <p role="alert" hidden></p>
      <button class="button" type="button" hidden>重试</button>
      <dl class="facts" hidden>
        <div><dt>业务数据源</dt><dd><span translate="no">OmniGrid Business API</span> · <span data-connection="omnigrid"></span></dd></div>
        <div><dt>Agent 运行环境</dt><dd><span translate="no">DeepSeek Harness</span> · <span data-connection="deepseek_harness"></span></dd></div>
      </dl>
    </section>`;

  const loadingHost = page.querySelector('[data-connections-loading]');
  const feedback = page.querySelector('[role="alert"]');
  const retry = page.querySelector('button');
  const facts = page.querySelector('dl');

  async function load() {
    retry.disabled = true;
    feedback.hidden = true;
    const loading = window.OmniRelayLoading.mount(loadingHost, { label: '正在读取连接设置…' });
    try {
      const response = await readConnections({ signal: AbortSignal.any([signal, AbortSignal.timeout(15000)]) });
      if (signal.aborted) return;
      if (!Array.isArray(response?.items)) throw new Error('连接设置响应无效。');
      const states = new Map(response.items.map(item => [item?.id, item?.state]));
      for (const field of facts.querySelectorAll('[data-connection]')) {
        field.textContent = states.get(field.dataset.connection) === 'not_configured' ? '未接入' : '状态未知';
      }
      facts.hidden = false;
      retry.hidden = true;
    } catch (error) {
      if (signal.aborted) return;
      const forbidden = error.status === 403;
      feedback.textContent = forbidden ? '当前账号没有查看连接设置的权限。' : '暂时无法读取连接设置，请重试。';
      feedback.hidden = false;
      retry.hidden = forbidden;
    } finally {
      loading.finish();
      retry.disabled = false;
    }
  }

  retry.addEventListener('click', load, { signal });
  void load();
}
