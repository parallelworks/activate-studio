import { useEffect, useState } from 'react'
import { DescriptionList, DescriptionListItem, ExternalLink } from '@parallelworks/ui'
import { DEFAULT_MARK } from './EmptyChatBrand'

const REPO_URL = 'https://github.com/parallelworks/activate-studio'
const GUFI_URL = 'https://github.com/mar-file-system/GUFI'

/**
 * Help, About: what this software is, its version, and who builds it. It
 * names ACTIVATE Studio whatever a deployment calls itself, since it is
 * about the software. The footer's version panel opens it too.
 */
export function About() {
  const [build, setBuild] = useState<{ version: string; commit: string | null } | null>(null)
  useEffect(() => {
    let stop = false
    fetch('/api/version').then(r => r.json())
      .then((b: { version: string; commit: string | null }) => { if (!stop) setBuild(b) })
      .catch(() => {})
    return () => { stop = true }
  }, [])
  return (
    <div className="about">
      <h1>About</h1>
      <div className="about-head">
        <img src={DEFAULT_MARK} alt="" width={48} height={48} />
        <div>
          <div className="about-name">ACTIVATE Studio</div>
          <div className="about-maker">
            Built and maintained by <ExternalLink href="https://parallelworks.com">Parallel Works</ExternalLink>
          </div>
        </div>
      </div>
      <DescriptionList variant="divided" className="about-list">
        <DescriptionListItem label="Version" mono>{build ? `${build.version}${build.commit ? ` (${build.commit})` : ''}` : '…'}</DescriptionListItem>
        <DescriptionListItem label="License">Apache License 2.0</DescriptionListItem>
        <DescriptionListItem label="Source"><ExternalLink href={REPO_URL}>github.com/parallelworks/activate-studio</ExternalLink></DescriptionListItem>
        <DescriptionListItem label="File index"><ExternalLink href={GUFI_URL}>GUFI</ExternalLink>, from Los Alamos National Laboratory</DescriptionListItem>
      </DescriptionList>
    </div>
  )
}
