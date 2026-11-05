import { useEffect, useState } from 'react'
import { api } from './api'

/** Every label in use with its count, read again whenever a label changes
 *  (`ade-tags-changed`), so a page that stays mounted does not go stale. */
export function useLabelVocabulary(): { tag: string; count: number }[] {
  const [vocab, setVocab] = useState<{ tag: string; count: number }[]>([])
  useEffect(() => {
    const load = () => { api.tagVocabulary().then(v => setVocab(v.tags)).catch(() => {}) }
    load()
    window.addEventListener('ade-tags-changed', load)
    return () => window.removeEventListener('ade-tags-changed', load)
  }, [])
  return vocab
}
