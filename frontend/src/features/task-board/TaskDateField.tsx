import { Input } from './controls'
import { openTaskDatePicker } from './task-date-picker'

// Keep the native calendar and keyboard date editing. The whole property row
// opens it on a user click, including the BoardUI field shell's padded area.
export function TaskDateField({ label, name, defaultValue, disabled, timezone = 'UTC' }: { label: string; name: string; defaultValue: string; disabled: boolean; timezone?: string }) {
  return <label className="task-property task-date-property" onClick={openTaskDatePicker} title={`日期时区：${timezone}`}><span>{label}</span><Input aria-label={label} aria-description={`日期时区：${timezone}`} type="date" name={name} defaultValue={defaultValue} disabled={disabled} /></label>
}
