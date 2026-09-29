import { cloneElement, useId, useEffect, useRef, type ReactNode, type ReactElement } from 'react'
import { X, AlertCircle } from 'lucide-react'
export function Badge({ children, tone = '' }: { children: ReactNode; tone?: string }) {
  return (
    <span className={`badge ${tone || String(children).toLowerCase()}`}>
      <i />
      {children}
    </span>
  )
}
export function Field({
  label,
  children,
}: {
  label: string
  children: ReactElement<{ id?: string }>
}) {
  const id = useId()
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {cloneElement(children, { id })}
    </div>
  )
}
export function ErrorBox({ children }: { children: ReactNode }) {
  return (
    <div role="alert" className="error">
      <AlertCircle size={17} />
      <span>{children}</span>
    </div>
  )
}
export function Modal({
  title,
  children,
  close,
}: {
  title: string
  children: ReactNode
  close: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const d = ref.current!
    d.showModal()
    return () => d.close()
  }, [])
  return (
    <dialog
      ref={ref}
      onCancel={(e) => {
        e.preventDefault()
        close()
      }}
      aria-label={title}
    >
      <header>
        <h2>{title}</h2>
        <button className="icon" aria-label="Close dialog" onClick={close}>
          <X size={20} />
        </button>
      </header>
      {children}
    </dialog>
  )
}
