import { ACCENTS, applyAccent } from '../accents'

/**
 * The accent colors as swatches, and a color input for any other. A pick
 * is applied at once and reported; the caller saves it. Settings, General
 * and the setup wizard.
 */
export function AccentSwatches({ value, onChange }: { value: string; onChange: (accent: string) => void }) {
  const pick = (v: string) => { applyAccent(v); onChange(v) }
  const custom = value.startsWith('custom:')
  return (
    <div className="accent-row" role="group" aria-label="Accent color">
      {Object.entries(ACCENTS).map(([k, a]) => (
        <button
          key={k}
          type="button"
          className={`accent-swatch ${value === k ? 'active' : ''}`}
          title={a.label}
          aria-label={a.label}
          aria-pressed={value === k}
          style={{ background: a.light[0] }}
          onClick={() => pick(k)}
        />
      ))}
      <input
        type="color"
        className={`accent-swatch accent-custom ${custom ? 'active' : ''}`}
        title="Custom accent"
        aria-label="Custom accent"
        value={custom ? value.slice(7) : '#06354f'}
        onChange={e => pick(`custom:${e.target.value}`)}
      />
    </div>
  )
}
