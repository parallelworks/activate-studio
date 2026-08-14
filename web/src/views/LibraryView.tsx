import { useState } from 'react'
import { Explorer } from '../components/Explorer'
import { Viewer } from '../components/Viewer'
import { UploadMenu } from '../components/UploadMenu'
import { useAppConfig } from '../config'

export function LibraryView({ selected, onSelect }: {
  selected: string | null
  onSelect: (path: string | null) => void
}) {
  const [targetDir, setTargetDir] = useState('uploads')
  const [refreshKey, setRefreshKey] = useState(0)
  const cfg = useAppConfig()

  return (
    <div className="library">
      <aside className="library-rail card">
        <div className="rail-head">
          <span className="rail-title">Knowledge base</span>
          <UploadMenu
            targetDir={targetDir}
            onTargetDir={setTargetDir}
            onDone={paths => {
              setRefreshKey(k => k + 1)
              if (paths[0]) onSelect(paths[0])
            }}
          />
        </div>
        <Explorer
          key={refreshKey}
          onOpen={onSelect}
          onDirFocus={dir => setTargetDir(dir || 'uploads')}
          selected={selected}
          rootLabel={cfg.kbLabel}
        />
      </aside>
      <section className="library-main card">
        <Viewer
          path={selected}
          onDeleted={() => { onSelect(null); setRefreshKey(k => k + 1) }}
        />
      </section>
    </div>
  )
}
