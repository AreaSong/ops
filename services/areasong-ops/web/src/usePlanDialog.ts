import { useEffect, useRef } from 'react'

// 两个计划弹窗共享焦点约束，关闭时恢复到原来的入口。
export function usePlanDialog<T extends HTMLElement>(pending: boolean, onCancel: () => void) {
  const ref = useRef<T>(null)
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    ref.current?.focus()
    return () => { if (previous?.isConnected) previous.focus() }
  }, [])
  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !pending) { event.preventDefault(); onCancel() }
      if (event.key !== 'Tab') return
      const elements = Array.from(dialog.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), [tabindex="0"]'))
      const first = elements[0], last = elements.at(-1)
      if (!first) { event.preventDefault(); dialog.focus(); return }
      if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog)) {
        event.preventDefault(); last?.focus()
      } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === dialog)) {
        event.preventDefault(); first.focus()
      }
    }
    dialog.addEventListener('keydown', keydown)
    return () => dialog.removeEventListener('keydown', keydown)
  }, [pending, onCancel])
  return ref
}
