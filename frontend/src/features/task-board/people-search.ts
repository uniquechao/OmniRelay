import { pinyin } from 'pinyin-pro'

export function normalizeSearch(text: string): string {
  return text.normalize('NFKC').toLocaleLowerCase('zh-CN').replace(/\s+/g, '')
}

// 仅索引姓名与组织。身份键由业务提供，不按姓名合并，也不读取其他私有字段。
export function peopleSearchText(person: { name: string; organizationPath?: string | null }): string {
  const name = person.name.trim()
  return [name, person.organizationPath ?? '', pinyin(name, { toneType: 'none' }), pinyin(name, { pattern: 'first', toneType: 'none' })].join(' ')
}

export function personAvatarText(name: string): string { return Array.from(name.trim()).slice(-2).join('') }
