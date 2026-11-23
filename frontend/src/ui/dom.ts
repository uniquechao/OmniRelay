export function requiredElement<T extends Element>(root:ParentNode,selector:string,ctor:{new():T}):T {
  const node=root.querySelector(selector)
  if(!(node instanceof ctor))throw new Error('缺少界面控件 '+selector)
  return node
}
export function closestControl(event:Event, selector:string) {
  return event.target instanceof Element ? event.target.closest<HTMLElement>(selector) : null
}
