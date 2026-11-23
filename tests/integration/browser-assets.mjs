import { readFile } from 'node:fs/promises';
import { extname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../', import.meta.url));
const types = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.png': 'image/png', '.svg': 'image/svg+xml' };
const policy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'";
export async function browserAsset(target, { setup = false } = {}) {
  const [path] = target.split('?');
  if (!path.startsWith('/') || /[%\\\x00]/.test(path) || path.split('/').some(part => part === '..' || part === '.')) {
    return { status: 404 };
  }
  const prototype = path.startsWith('/prototype/');
  const origin = process.env.OMNIRELAY_ASSET_ORIGIN;
  if (!prototype && origin) {
    const res = await fetch(new URL(origin).origin + target, { redirect: 'manual' });
    const headers = {};
    for (const key of ['content-type', 'content-security-policy', 'location']) {
      if (res.headers.has(key)) headers[key] = res.headers.get(key);
    }
    return { status: res.status, headers, body: Buffer.from(await res.arrayBuffer()) };
  }
  if (setup && path === '/' && new URLSearchParams(target.split('?')[1]).get('setup') !== 'complete') {
    return { status: 303, headers: { location: '/setup' } };
  }
  const name = setup && (path === '/setup' || path === '/') ? 'setup.html' : path.slice(1) || 'index.html';
  let file;
  if (prototype) file = resolve(root, 'design', '.' + path);
  else {
    if (path !== '/' && !(setup && path === '/setup') && !path.startsWith('/assets/')) return { status: 404 };
    file = resolve(process.env.OMNIRELAY_BROWSER_DIST || resolve(root, 'internal/webui/dist'), './' + name);
  }
  try {
    return { status: 200, body: await readFile(file), headers: {
      'content-type': types[extname(file)] || 'application/octet-stream',
      ...(prototype ? {} : { 'content-security-policy': policy })
    }};
  } catch (error) {
    if (['ENOENT', 'ENOTDIR', 'EISDIR'].includes(error.code)) return { status: 404 };
    throw error;
  }
}
