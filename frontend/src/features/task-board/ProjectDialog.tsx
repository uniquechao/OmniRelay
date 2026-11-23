import { useId, type ReactNode } from 'react'
import { Modal } from './controls'
import { Button } from './controls'
import { DialogHeader, DialogTitle } from './controls'
import { Icon } from './controls'

export function ProjectDialog({ title, children, onClose, busy = false, wide = false }: { title: string; children: ReactNode; onClose: () => void; busy?: boolean; wide?: boolean }) {
  const id = useId()
  return <Modal className={`project-dialog ${wide ? 'project-dialog-wide sm:max-w-[65rem]' : ''}`} backdropClassName="dialog-backdrop" labelledBy={id} onClose={onClose} busy={busy}>
    <DialogHeader className="flex flex-row items-center justify-between gap-4 text-start"><DialogTitle id={id}>{title}</DialogTitle><Button variant="ghost" size="icon" className="dialog-close" type="button" aria-label="关闭" disabled={busy} onClick={onClose}><Icon name="action-close" /></Button></DialogHeader>
    {children}
  </Modal>
}
