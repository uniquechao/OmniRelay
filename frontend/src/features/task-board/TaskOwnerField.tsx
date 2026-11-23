import { useContext, useState, type RefObject, type ReactNode } from 'react'
import { Button, ComboBox, ComboBoxStateContext, Input, Label, ListBox, ListBoxItem, Popover, Text } from 'react-aria-components'
import { ChevronDown } from 'lucide-react'
import { normalizeSearch, peopleSearchText } from './people-search'
import type { ProjectMember } from './api'

type Option = { id: string; name: string; organization?: string; search: string }

// A task owner has a separate uncommitted query, as in project-tasks.js.
// React Aria owns the popup/focus; arbitrary text is never a member identity.
export function TaskOwnerField({ members, value, disabled, inputRef, onChange, onQueryChange }: {
  members: ProjectMember[]; value: string | null; disabled: boolean; inputRef: RefObject<HTMLInputElement | null>
  onChange: (id: string | null) => void; onQueryChange: (query: string) => void
}) {
  const options: Option[] = [{ id: '', name: '未分配', search: '未分配' }, ...members.map(member => ({ id: member.id, name: member.display_name, organization: member.organization_path, search: peopleSearchText({ name: member.display_name, organizationPath: member.organization_path }) }))]
  const label = options.find(item => item.id === (value ?? ''))?.name ?? ''
  const [query, setQuery] = useState(label)
  const [unselected, setUnselected] = useState(false)
  const term = !unselected && query === label ? '' : normalizeSearch(query)
  const matches = options.filter(item => normalizeSearch(item.search).includes(term))
  return <ComboBox className="prototype-combobox" items={matches} selectedKey={unselected ? null : value ?? ''} inputValue={query} onInputChange={setQuery}
    onSelectionChange={key => {
      if (key === null) return
      const id = String(key)
      setUnselected(false); setQuery(options.find(item => item.id === id)?.name ?? '')
      onChange(id || null); onQueryChange('')
    }} allowsCustomValue allowsEmptyCollection isDisabled={disabled} menuTrigger="input"
    onKeyDown={event => { if (event.key === 'Escape' && inputRef.current?.getAttribute('aria-expanded') !== 'true') event.continuePropagation() }}>
    <Label>负责人</Label>
    <OwnerComboField>
      <Input name="owner-search" ref={inputRef} placeholder="搜索姓名或拼音" onInput={event => {
        const text = event.currentTarget.value
        setUnselected(true); onQueryChange(text.trim())
        if (!text.trim()) onChange(null)
      }} />
      {!unselected && value && <span className="task-owner-avatar" aria-hidden>{Array.from(label).slice(-2).join('')}</span>}
      <Button className="prototype-combo-toggle" aria-label="展开人员选项"><ChevronDown size={14} aria-hidden /></Button>
    </OwnerComboField>
    <Popover className="prototype-picker" offset={4} data-prototype-overlay="picker">
      <ListBox<Option> className="prototype-options" aria-label="人员选项" renderEmptyState={() => <span className="prototype-empty">没有匹配选项</span>}>
        {item => <ListBoxItem id={item.id} textValue={item.name} className="prototype-option"><Text slot="label">{item.name}</Text>{item.organization && <Text slot="description">{item.organization}</Text>}</ListBoxItem>}
      </ListBox>
    </Popover>
  </ComboBox>
}
function OwnerComboField({ children }: { children: ReactNode }) {
  const state = useContext(ComboBoxStateContext)
  return <div className="prototype-combo-field" onClick={event => {
    if (event.target instanceof HTMLInputElement && !state?.isOpen) state?.open()
  }} onKeyDownCapture={event => {
    if (event.key === 'Escape' && state?.isOpen && !event.nativeEvent.isComposing) { event.preventDefault(); event.stopPropagation(); state.close() }
  }}>{children}</div>
}
