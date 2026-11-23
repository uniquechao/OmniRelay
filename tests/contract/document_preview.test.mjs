import assert from 'node:assert/strict';
import test from 'node:test';
import {renderResult} from '../../frontend/src/features/drive/drive-preview.ts';
test('文档保留标题、强调、列表、表格和图片位置',()=>{
 const html=renderResult('docs',{document:[
  {type:'heading',level:2,runs:[{text:'标题',bold:true}]},
  {type:'bullet',runs:[{text:'一'}]},{type:'bullet',runs:[{text:'二',italic:true}]},
  {type:'table',rows:[[[{type:'text',runs:[{text:'单元格'}]}]]]},
  {type:'image',image:{mime:'image/png',data:'abc'}},
  {type:'code',runs:[{text:'<script>secret</script>'}]}]},{});
 assert.match(html,/<h2><strong>标题<\/strong><\/h2>/);
 assert.match(html,/<ul><li>一<\/li><li><em>二<\/em><\/li><\/ul>/);
 assert.match(html,/<td><p>单元格<\/p><\/td>/);
 assert.match(html,/canvas data-document-image="0"/);
 assert.doesNotMatch(html,/<script>/);
});
test('文档链接和文字转义，旧纯文本预览继续可用',()=>{
 const html=renderResult('docs',{document:[{type:'text',runs:[{text:'<img onerror=alert(1)>',link:'javascript:alert(1)'},{text:'站点',link:'https://example.test/?q="'}]}]},{});
 assert.doesNotMatch(html,/href="javascript:/);
 assert.doesNotMatch(html,/<img/);
 assert.match(html,/rel="noopener noreferrer"/);
 assert.match(renderResult('docs',{text:'旧文档'},{}),/旧文档/);
});
