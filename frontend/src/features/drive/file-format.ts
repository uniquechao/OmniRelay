import * as workbenchUI from '../../ui/widgets';
const monograms = new Map([['pdf','PDF'], ['word','W'], ['excel','X'], ['slides','P']]);
const symbols = new Map([['zip','file-zip'], ['base','file-grid'], ['minutes','file-minutes'], ['docs','nav-logs']]);

export function fileIcon(key:string, label:string) {
  const { escape, icon } = workbenchUI;
  const type = monograms.has(key) || symbols.has(key) ? key : 'generic';
  const graphic = monograms.has(type) ? '<span>' + monograms.get(type) + '</span>' : icon(symbols.get(type) || 'nav-logs');
  return '<span class="relay-file-icon" data-file-type="' + type + '" role="img" aria-label="' +
    escape(label || '其他文件') + '">' + graphic + '</span>';
}
