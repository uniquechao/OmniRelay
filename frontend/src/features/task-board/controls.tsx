import {useLayoutEffect} from 'react'
import type {ComponentProps, ReactNode, RefObject} from 'react'
import * as Pop from '@radix-ui/react-popover'
import * as Menu from '@radix-ui/react-dropdown-menu'
import {Dialog, Modal as AriaModal, ModalOverlay} from 'react-aria-components'
import {X} from 'lucide-react'
import './controls.css'

export function Button({variant='default',size,...props}:ComponentProps<'button'> & {variant?:string;size?:string}) {
  return <button type="button" data-prototype-control="button" data-prototype-variant={variant} data-prototype-size={size} {...props}/>
}
export const Input = (props:ComponentProps<'input'>) => <input data-prototype-control="input" {...props}/>
export const NativeSelect = (props:ComponentProps<'select'>) => <select data-prototype-control="select" {...props}/>
export const Textarea = (props:ComponentProps<'textarea'>) => <textarea data-prototype-control="textarea" {...props}/>
export const Popover=Pop.Root, PopoverTrigger=Pop.Trigger, PopoverAnchor=Pop.Anchor
export function PopoverContent(props:ComponentProps<typeof Pop.Content>) {
  return <Pop.Portal><Pop.Content sideOffset={4} data-board-menu="" {...props}/></Pop.Portal>
}
export const DropdownMenu=Menu.Root, DropdownMenuTrigger=Menu.Trigger, DropdownMenuItem=Menu.Item
export function DropdownMenuContent(props:ComponentProps<typeof Menu.Content>) {
  return <Menu.Portal><Menu.Content sideOffset={4} data-board-menu="" {...props}/></Menu.Portal>
}
export const DialogHeader='header', DialogTitle='h2'
export const Icon=(_: {name:string})=><X size={16} aria-hidden/>
export function Modal({children,className,labelledBy,busy,onClose,initialFocus,presentation,returnTo}:{
  children:ReactNode;className:string;labelledBy:string;busy?:boolean;onClose:()=>void;
  returnTo?:HTMLElement|null;initialFocus?:RefObject<HTMLElement|null>;presentation?:string;backdropClassName?:string
}) {
  useLayoutEffect(() => {
    initialFocus?.current?.focus({preventScroll:true})
    return () => {
      if (returnTo) requestAnimationFrame(() => {
        if (returnTo.isConnected) returnTo.focus({preventScroll:true})
      })
    }
  }, [initialFocus, returnTo])
  return <ModalOverlay isOpen isDismissable={!busy} isKeyboardDismissDisabled={busy}
    className="task-settings-overlay" onOpenChange={open => { if(!open && !busy) onClose() }}>
    <AriaModal>
      <Dialog className={'task-board-modal '+className} data-presentation={presentation} aria-labelledby={labelledBy}>{children}</Dialog>
    </AriaModal>
  </ModalOverlay>
}
