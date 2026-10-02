import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'

/**
 * A "Select chats" row in the chat package's rail, under New chat and
 * Attachments. The package has no slot for extra rows, so this finds that
 * group in the rendered rail and portals a row styled like its siblings
 * into it, re-attaching if the package re-renders the group. It copies the
 * label class from the New chat row so it collapses with the rail.
 */

const NEW_CHAT = 'button[title="New chat"]'

export function ManageChatsRailItem({ active, onSelect }: { active: boolean; onSelect: () => void }) {
  const [host, setHost] = useState<HTMLElement | null>(null)
  const [labelClass, setLabelClass] = useState('text-sm')

  useEffect(() => {
    const canvas = document.querySelector('.chat-canvas')
    if (!canvas) return
    const el = document.createElement('div')
    el.className = 'ade-rail-extra'
    const place = () => {
      const anchor = canvas.querySelector<HTMLButtonElement>(NEW_CHAT)
      const group = anchor?.parentElement
      if (!group) return
      if (el.parentElement !== group) group.appendChild(el)
      const span = anchor.querySelector('span')
      if (span && span.className !== labelClass) setLabelClass(span.className)
      setHost(el)
    }
    place()
    const watch = new MutationObserver(place)
    watch.observe(canvas, { childList: true, subtree: true, attributes: true, attributeFilter: ['class'] })
    return () => { watch.disconnect(); el.remove() }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  if (!host) return null
  return createPortal(
    <button
      type="button"
      className={`flex items-center h-9 px-2 gap-3 rounded-lg hover:theme-muted-panel w-full ade-rail-select${active ? ' active' : ''}`}
      title="Select chats to delete"
      aria-pressed={active}
      onClick={onSelect}
    >
      <svg className="flex-shrink-0 w-4 h-4" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true">
        <rect x="2" y="2.5" width="4" height="4" rx="1" />
        <path d="m2.9 11.4 1 1 1.9-2.1" />
        <path d="M8.5 4.5h5.5M8.5 11.5h5.5" />
      </svg>
      <span className={labelClass}>Select chats</span>
    </button>,
    host,
  )
}
