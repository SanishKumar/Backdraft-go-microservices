import { useEffect, useState } from 'react'

type ToastMsg = { text: string; error: boolean; id: number }
let push: ((t: ToastMsg) => void) | null = null
let seq = 0

export function toast(text: string, error = false) {
  push?.({ text, error, id: ++seq })
}

export function Toaster() {
  const [msg, setMsg] = useState<ToastMsg | null>(null)
  useEffect(() => {
    push = setMsg
    return () => {
      push = null
    }
  }, [])
  useEffect(() => {
    if (!msg) return
    const id = window.setTimeout(() => setMsg(null), 3800)
    return () => window.clearTimeout(id)
  }, [msg])
  if (!msg) return null
  return (
    <div className={`toast panel${msg.error ? ' error' : ''}`} role="status">
      {msg.text}
    </div>
  )
}
