import { Button, Modal, toast } from '@parallelworks/ui'
import { reloadAppConfig } from '../config'
import { CopyLibraryForm } from './CopyLibraryForm'
import { addedMessage } from './LibraryAddDialogs'

/**
 * Keep a local copy of a folder in a Transfers location and index it: for
 * a host that takes SFTP only, a cloud drive or another rclone remote,
 * where the Studio cannot build an index. It lives in Transfers, where
 * those locations are chosen and browsed, rather than in Add a library,
 * whose libraries are read where they are. The copy is read only; it is
 * listed with the other libraries in Settings, Indexing, Libraries, where
 * it is updated and removed. Offered on a Studio with sign-in off.
 */
export function KeepCopyDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  return (
    <Modal open={open} onClose={onClose} size="lg" title="Keep a local copy and index it"
      description="A folder in one of your Transfers locations, copied to this computer and indexed, then looked at again on an interval. Read only: put files in the location itself, and they arrive at the next look."
      footer={<Button variant="secondary" onClick={onClose}>Close</Button>}>
      {open && (
        <CopyLibraryForm onAdded={lib => {
          toast.success(addedMessage(lib, true, true))
          reloadAppConfig()
          onClose()
        }} />
      )}
    </Modal>
  )
}
