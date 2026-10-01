import { useEffect, useMemo, useState, type ComponentProps } from 'react'
import { FieldWrapper, type FieldComponent } from '@parallelworks/ui/form'

/**
 * Field components the platform's DynamicForm leaves to the host: the
 * pickers backed by platform data, and a few types the core registry
 * does not draw. Each takes the same props as the package's own fields.
 *
 * A cluster is held in the form as an object, because the workflows'
 * own conditions read its properties (inputs.resource.schedulerType),
 * and carries its pw:// reference in `_studioRef`, which the server
 * submits in its place for the platform to expand.
 */

interface ClusterOption { value: string; name: string; label: string; status: string; scheduler: string | null; user?: string | null; type?: string | null }

const cache = new Map<string, Promise<unknown>>()
function load<T>(url: string): Promise<T> {
  if (!cache.has(url)) cache.set(url, fetch(url).then(r => (r.ok ? r.json() : Promise.reject(new Error(String(r.status))))).catch(e => { cache.delete(url); throw e }))
  return cache.get(url) as Promise<T>
}
/** Forget platform data so the next form open re-reads it. */
export function forgetPlatformData(): void { cache.clear() }

/** Top-level fields get no onChange from the form; the built-in fields
 *  write through Formik themselves, and these do the same. */
function write(p: ComponentProps<FieldComponent>, v: unknown): void {
  const name = String((p.field as { name?: string }).name ?? '')
  p.onChange?.(v)
  if (name) { p.setFieldValue?.(name, v); p.setFieldTouched?.(name, true) }
  p.setFormDirty?.(true)
}

/** The value an expression like `${{ inputs.resource }}` points at. */
function resolveRef(expr: unknown, values: Record<string, unknown>): unknown {
  const m = /\$\{\{\s*inputs\.([A-Za-z0-9_.]+)\s*\}\}/.exec(String(expr ?? ''))
  if (!m) return undefined
  return m[1].split('.').reduce<unknown>((o, k) => (o && typeof o === 'object' ? (o as Record<string, unknown>)[k] : undefined), values)
}
function clusterName(v: unknown): string {
  if (!v) return ''
  if (typeof v === 'string') return v.replace(/^pw:\/\/[^/]+\//, '')
  return String((v as { name?: unknown }).name ?? '')
}

function Select({ label, field, value, options, onChange, disabled, placeholder, note }: {
  label: string; field: { optional?: unknown; tooltip?: unknown }; value: string
  options: { value: string; label: string }[]; onChange: (v: string) => void; disabled: boolean; placeholder: string; note?: string
}) {
  return (
    <FieldWrapper label={label} optional={field.optional === true} description={note}>
      {(b) => (
        <select id={b.id} aria-describedby={b.describedBy} className="field wf-select" value={value} disabled={disabled}
          onChange={e => onChange(e.target.value)}>
          <option value="">{placeholder}</option>
          {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
      )}
    </FieldWrapper>
  )
}

const ClusterField: FieldComponent = (props) => {
  const { field, label, currentValue, disabled } = props
  const onChange = (v: unknown) => write(props, v)
  const [opts, setOpts] = useState<ClusterOption[]>([])
  const [err, setErr] = useState('')
  useEffect(() => { load<{ clusters: ClusterOption[] }>('/api/platform/clusters').then(d => setOpts(d.clusters)).catch(e => setErr(String(e.message))) }, [])
  const pick = (ref: string) => {
    const c = opts.find(o => o.value === ref)
    // name and user are what the platform's submission step turns into the
    // pw:// reference, so a cluster shared by someone else keeps its owner.
    onChange(c ? { name: c.name, user: c.user ?? undefined, type: c.type ?? undefined, schedulerType: c.scheduler ?? '', status: c.status, _studioRef: c.value } : '')
  }
  const cv = (typeof currentValue === 'object' && currentValue ? currentValue : null) as { _studioRef?: string; name?: string; user?: string } | null
  const current = cv ? String(cv._studioRef ?? (cv.user && cv.name ? `pw://${cv.user}/${cv.name}` : cv.name ?? '')) : String(currentValue ?? '')
  // autoselect: the platform form picks the first usable cluster on open.
  useEffect(() => {
    if (!current && (field as { autoselect?: unknown }).autoselect && opts.length) {
      const first = opts.find(o => o.status === 'active' || o.status === 'on') ?? opts[0]
      pick(first.value)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [opts])
  return (
    <Select label={label} field={field} value={current} disabled={disabled} placeholder={err ? 'Clusters unavailable' : 'Select a cluster'}
      options={opts.map(o => ({ value: o.value, label: `${o.label}${o.status && o.status !== 'active' ? ` (${o.status})` : ''}` }))}
      onChange={pick} note={err ? `Could not list clusters: ${err}` : undefined} />
  )
}

const PartitionField: FieldComponent = (props) => {
  const { field, label, currentValue, values, disabled } = props
  const onChange = (v: unknown) => write(props, v)
  const cluster = clusterName(resolveRef((field as { resource?: unknown }).resource ?? '${{ inputs.resource }}', values))
  const [opts, setOpts] = useState<{ value: string; label: string }[]>([])
  useEffect(() => {
    if (!cluster) { setOpts([]); return }
    load<{ partitions: { value: string; label: string; state: string; availNodes: number | null; totalNodes: number | null }[] }>(`/api/platform/partitions?cluster=${encodeURIComponent(cluster)}`)
      .then(d => setOpts(d.partitions.map(p => ({ value: p.value, label: `${p.label}${p.state ? `, ${p.state}` : ''}${p.availNodes != null ? `, ${p.availNodes}/${p.totalNodes} nodes free` : ''}` }))))
      .catch(() => setOpts([]))
  }, [cluster])
  return <Select label={label} field={field} value={String(currentValue ?? '')} disabled={disabled || !cluster}
    placeholder={cluster ? 'Select a partition' : 'Choose a cluster first'} options={opts} onChange={v => onChange(v)} />
}

const BucketField: FieldComponent = (props) => {
  const { field, label, currentValue, disabled } = props
  const onChange = (v: unknown) => write(props, v)
  const [opts, setOpts] = useState<{ value: string; label: string }[]>([])
  useEffect(() => { load<{ buckets: { value: string; label: string }[] }>('/api/platform/buckets').then(d => setOpts(d.buckets)).catch(() => setOpts([])) }, [])
  const current = typeof currentValue === 'object' && currentValue ? String((currentValue as { _studioRef?: string; uri?: string })._studioRef ?? (currentValue as { uri?: string }).uri ?? '') : String(currentValue ?? '')
  return <Select label={label} field={field} value={current} disabled={disabled} placeholder="Select a bucket" options={opts}
    onChange={v => onChange(v ? { _studioRef: v, uri: v } : '')} />
}

/** A plain text control for types whose options the public API does not expose. */
function textField(kind: 'text' | 'password' | 'textarea', hint?: string): FieldComponent {
  const F: FieldComponent = (props) => {
    const { field, label, currentValue, disabled } = props
    const onChange = (v: unknown) => write(props, v)
    return (
      <FieldWrapper label={label} optional={field.optional === true} description={hint}>
        {(b) => kind === 'textarea'
          ? <textarea id={b.id} aria-describedby={b.describedBy} className="field wf-editor" rows={8} disabled={disabled}
              value={String(currentValue ?? '')} onChange={e => onChange(e.target.value)} />
          : <input id={b.id} aria-describedby={b.describedBy} className="field" type={kind} disabled={disabled}
              placeholder={String((field as { placeholder?: unknown }).placeholder ?? '')}
              value={String(currentValue ?? '')} onChange={e => onChange(e.target.value)} />}
      </FieldWrapper>
    )
  }
  return F
}

export function useWorkflowFields(): Record<string, FieldComponent> {
  return useMemo(() => ({
    'compute-clusters': ClusterField,
    'compute-resources': ClusterField,
    dynamicPartitionDropdown: PartitionField,
    dynamicAccountDropdown: textField('text', 'The scheduler account; a saved configuration fills this in for the site.'),
    dynamicQOSDropdown: textField('text', 'Quality of service, if the site uses one.'),
    bucket: BucketField,
    'kubernetes-clusters': textField('text'),
    'kubernetes-namespaces': textField('text'),
    'kubernetes-pvc': textField('text'),
    instances: textField('text'),
    editor: textField('textarea'),
    password: textField('password'),
  }), [])
}
