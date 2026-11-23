import { useMemo, useState } from 'react'
import { Button, Dialog, DialogTrigger, Popover } from 'react-aria-components'
import { ChevronDown } from 'lucide-react'
import type { ProjectMember } from './api'
import { normalizeSearch, peopleSearchText, personAvatarText } from './people-search'

export function TaskParticipantsField({ members, value, onChange, disabled, interactions = [] }: {
  interactions?: ProjectMember[]; members: ProjectMember[]; value: string[]; onChange: (ids: string[]) => void; disabled: boolean
}) {
  const [query, setQuery] = useState('')
  const automatic = new Set(interactions.map(person => person.id))
  const ids = [...new Set([...value, ...automatic])]
  const choices = useMemo(() => [...new Map([...interactions, ...members].map(person => [person.id, person])).values()], [members, interactions])
  const people = useMemo(() => choices.map(member => ({
    ...member, search: normalizeSearch(peopleSearchText({name: member.display_name, organizationPath: member.organization_path})),
  })), [choices])
  const matches = people.filter(person => (person.active !== false || ids.includes(person.id)) && person.search.includes(normalizeSearch(query)))
  const selected = ids.map(id => people.find(person => person.id === id)?.display_name || '原项目成员')
  return <div className="task-property task-participants">
    <span>参与人</span>
    <DialogTrigger onOpenChange={() => setQuery('')}>
      <Button className="participants-trigger" aria-label={'参与人，' + (selected.join('、') || '未选择')} isDisabled={disabled}>
        <span className="participant-values">{selected.length ? selected.map((name, index) =>
          <span className="participant-chip" key={ids[index]}>{name}</span>) : '选择参与人'}</span>
        <ChevronDown size={14} aria-hidden />
      </Button>
      <Popover className="participants-popover prototype-picker" placement="bottom end" offset={4}>
        <Dialog aria-label="选择任务参与人" className="participants-dialog">
          <input autoFocus type="search" aria-label="搜索参与人" placeholder="搜索姓名或拼音" value={query} onChange={event => setQuery(event.target.value)} />
          <div className="participants-options">{matches.map(person =>
            <label key={person.id}><input type="checkbox" checked={ids.includes(person.id)} disabled={disabled || automatic.has(person.id)} onChange={event => onChange(event.target.checked ? [...value, person.id] : value.filter(id => id !== person.id))} />
              <span className="participant-avatar" aria-hidden>{personAvatarText(person.display_name)}</span><span>{person.display_name}</span>{automatic.has(person.id) && <small>互动加入</small>}</label>)}
            {!matches.length && <p>没有匹配的成员</p>}
          </div>
        </Dialog>
      </Popover>
    </DialogTrigger>
  </div>
}
