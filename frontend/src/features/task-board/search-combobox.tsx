import { useContext, useEffect, useEffectEvent, useId, useRef, useState, type ReactNode, type Ref, type RefObject } from 'react'
import { Button, ComboBox, ComboBoxStateContext, Input, Label, ListBox, ListBoxItem, Popover, Text } from 'react-aria-components'
import { mergeRefs } from 'react-aria/mergeRefs'
import { ChevronDown, X } from 'lucide-react'
import { normalizeSearch } from './people-search'
import { inheritsFieldsetDisabled, useFieldsetDisabled } from './use-fieldset-disabled'

export type SearchComboboxProps<T> = {
  items: readonly T[]
  getKey: (item: T) => string
  getLabel: (item: T) => string
  getSearchText?: (item: T) => string
  getDescription?: (item: T) => string | undefined | null
  value: string | null
  onChange: (value: string | null) => void
  allowCustom?: boolean
  onCustomValue?: (text: string) => void
  disabled?: boolean
  label: string
  placeholder?: string
  description?: string
  error?: string
  name?: string
  required?: boolean
  inputRef?: Ref<HTMLInputElement>
  clearOnSelect?: boolean
}

type ComboOption = { id: string; label: string; description?: string | null; search: string }

export function SearchCombobox<T>({ items, getKey, getLabel, getSearchText = getLabel, getDescription, value, onChange, allowCustom = false, onCustomValue, disabled = false, label, placeholder = '搜索或选择', description, error, name, required, inputRef, clearOnSelect = false }: SearchComboboxProps<T>) {
  const input = useRef<HTMLInputElement>(null)
  const fieldsetDisabled = useFieldsetDisabled(input)
  const blocked = () => disabled || inheritsFieldsetDisabled(input.current)
  const id = useId()
  const selectedLabel = items.find(item => getKey(item) === value)
  const labelValue = selectedLabel === undefined ? (allowCustom ? value ?? '' : '') : getLabel(selectedLabel)
  const [query, setQuery] = useState(labelValue)
  const [closeRevision, setCloseRevision] = useState(0)
  const [previousValue, setPreviousValue] = useState(value)
  if (previousValue !== value) { setPreviousValue(value); setQuery(labelValue) }
  const options: ComboOption[] = items.map(item => ({ id: getKey(item), label: getLabel(item), description: getDescription?.(item), search: getSearchText(item) }))
  if (allowCustom && value !== null && !options.some(option => option.id === value)) options.push({ id: value, label: value, search: value })
  const term = query === labelValue ? '' : normalizeSearch(query)
  const matches = options.filter(option => normalizeSearch(option.search).includes(term))
  // React Aria拥有输入、active descendant、IME、门户及所属焦点域；不另建焦点管理器。
  return <ComboBox className="prototype-combobox" name={name} selectedKey={value} onSelectionChange={key => { if (!blocked()) { onChange(key === null ? null : String(key)); setQuery(key === null || clearOnSelect ? '' : options.find(option => option.id === key)?.label ?? ''); if (key !== null) setCloseRevision(revision => revision + 1) } }}
    onKeyDown={event => { if (event.key === 'Escape' && input.current?.getAttribute('aria-expanded') !== 'true') event.continuePropagation() }}
    items={matches} inputValue={query} onInputChange={setQuery} allowsCustomValue={allowCustom} allowsEmptyCollection menuTrigger="input" isDisabled={disabled || fieldsetDisabled} isRequired={required} isInvalid={!!error}
>
    <Label>{label}</Label>
    <ComboField closeRevision={closeRevision} disabled={disabled || fieldsetDisabled} input={input} allowCustom={allowCustom} onCustomValue={onCustomValue ?? onChange} knownLabels={options.map(option => option.label)} blocked={blocked}>
      <Input id={id} ref={mergeRefs(input, inputRef)} placeholder={placeholder} />
      <Button aria-label={`展开${label}选项`} className="prototype-combo-toggle"><ChevronDown size={14} aria-hidden /></Button>
    </ComboField>
    {description && <Text className="ui-hint" slot="description">{description}</Text>}
    {error && <Text slot="errorMessage" className="prototype-field-error">{error}</Text>}
    <Popover className="prototype-picker" offset={4} data-prototype-overlay="picker">
      <ListBox<ComboOption> className="prototype-options" aria-label={`${label}选项`} renderEmptyState={() => <span className="prototype-empty">没有匹配选项</span>}>
        {option => <ListBoxItem id={option.id} textValue={option.label} className="prototype-option">
          <Text slot="label">{option.label}</Text>{option.description && <Text slot="description">{option.description}</Text>}
        </ListBoxItem>}
      </ListBox>
    </Popover>
  </ComboBox>
}

function ComboField({ closeRevision, children, disabled, input, allowCustom, onCustomValue, knownLabels, blocked }: { closeRevision: number; children: ReactNode; disabled: boolean; input: RefObject<HTMLInputElement | null>; allowCustom: boolean; onCustomValue?: (text: string) => void; knownLabels: string[]; blocked: () => boolean }) {
  const state = useContext(ComboBoxStateContext)
  const close = useEffectEvent(() => state?.close())
  useEffect(() => { if (disabled || closeRevision > 0) close() }, [disabled, closeRevision])
  return <div className="prototype-combo-field" onPointerDownCapture={event => { if (blocked()) { event.preventDefault(); event.stopPropagation() } }} onKeyDownCapture={event => {
      if (blocked()) { event.preventDefault(); event.stopPropagation(); return }
      if (event.key === 'Escape' && state?.isOpen) { event.preventDefault(); event.stopPropagation(); state.close(); return }
      if (event.key === 'Enter' && !event.nativeEvent.isComposing && allowCustom && onCustomValue && !input.current?.getAttribute('aria-activedescendant')) {
        const text = input.current?.value.trim()
        if (text && !knownLabels.includes(text)) { event.preventDefault(); event.stopPropagation(); onCustomValue(text); state?.close() }
      }
    }}>{children}</div>
}

export function SearchMultiSelect<T>({ value, onChange, ...props }: Omit<SearchComboboxProps<T>, 'value' | 'onChange' | 'allowCustom' | 'onCustomValue'> & { value: readonly string[]; onChange: (value: string[]) => void }) {
  const input = useRef<HTMLInputElement>(null)
  return <div className="prototype-multiselect">
    <ul className="prototype-tags" aria-label={`已选${props.label}`}>
      {value.map(key => { const item = props.items.find(item => props.getKey(item) === key); return <li key={key}><span>{item ? props.getLabel(item) : '已选项不可用'}</span><button type="button" disabled={props.disabled} aria-label={`移除${item ? props.getLabel(item) : '不可用项'}`} onClick={event => {
        if (inheritsFieldsetDisabled(event.currentTarget)) return
        onChange(value.filter(selected => selected !== key)); input.current?.focus()
      }}><X size={14} aria-hidden /></button>{props.name && <input type="hidden" name={props.name} value={key} disabled={props.disabled} />}</li> })}
    </ul>
    <SearchCombobox {...props} name={undefined} items={props.items.filter(item => !value.includes(props.getKey(item)))} inputRef={input} clearOnSelect value={null} onChange={key => { if (key !== null) { onChange([...value, key]) } }} />
  </div>
}
