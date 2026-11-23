import { readdirSync, readFileSync } from 'node:fs';
import { dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';

const source = fileURLToPath(new URL('./src/', import.meta.url));
const prototype = fileURLToPath(new URL('../design/prototype/', import.meta.url));
const failures = [];

function inspect(directory) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const file = join(directory, entry.name);
    if (entry.isDirectory()) {
      inspect(file);
      continue;
    }
    if (/\.[cm]?jsx?$/i.test(file)) {
      failures.push(relative(source, file) + ' 应使用 TypeScript');
      continue;
    }
    if (!/\.[cm]?tsx?$/.test(file)) continue;
    const unit = ts.createSourceFile(file, readFileSync(file, 'utf8'), ts.ScriptTarget.Latest, true);
    function check(node) {
      if (ts.isStringLiteralLike(node) && node.text.startsWith('.')) {
        const resource = resolve(dirname(file), node.text.split(/[?#]/, 1)[0]);
        const local = relative(prototype, resource);
        const fromPrototype = !isAbsolute(local) && !local.startsWith('..' + sep);
        const staticAsset = /\.(css|svg|png|jpe?g|webp|ico|woff2?)$/i.test(resource);
        if (fromPrototype && !staticAsset) {
          failures.push(relative(source, file) + ' 引用了原型脚本 ' + node.text);
        }
      }
      ts.forEachChild(node, check);
    }
    check(unit);
  }
}

inspect(source);
if (failures.length) {
  console.error(failures.join('\n'));
  process.exitCode = 1;
}
