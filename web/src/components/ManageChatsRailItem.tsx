/**
 * "Select chats" at the top of the conversation list. ChatLayout's
 * sidebarTop slot renders it above the package's own New chat, Attachments
 * and search, in the column, on the rail and in the phone drawer alike. It
 * uses the package's row classes so it reads as one of those rows, and on
 * the rail it drops to its icon the way they do.
 */
export function ManageChatsRailItem({ active, collapsed, onSelect }: {
  active: boolean
  collapsed: boolean
  onSelect: () => void
}) {
  return (
    <div className="ade-rail-top">
      <button
        type="button"
        className={`flex items-center h-9 px-2 gap-3 rounded-lg w-full transition-colors hover:chat-tint ade-rail-select${active ? ' active' : ''}`}
        title="Select chats to delete"
        aria-pressed={active}
        onClick={onSelect}
      >
        <svg className="flex-shrink-0 w-4 h-4" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true">
          <rect x="2" y="2.5" width="4" height="4" rx="1" />
          <path d="m2.9 11.4 1 1 1.9-2.1" />
          <path d="M8.5 4.5h5.5M8.5 11.5h5.5" />
        </svg>
        <span className={`text-sm transition-opacity duration-200 ${collapsed ? 'opacity-0 w-0 overflow-hidden' : 'opacity-100'}`}>Select chats</span>
      </button>
    </div>
  )
}
