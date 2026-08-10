export type Labels = Record<string, string>

function escapeLabelValue(value: string): string {
  return value.replace(/\\/g, '\\\\').replace(/"/g, '\\"').replace(/\n/g, '\\n')
}

export function formatLabels(labels: Labels): string {
  const entries = Object.entries(labels).filter(([, value]) => value !== '')
  if (entries.length === 0) return ''
  const inner = entries.map(([key, value]) => `${key}="${escapeLabelValue(value)}"`).join(',')
  return `{${inner}}`
}

function seriesKey(name: string, labels: Labels): string {
  const sorted = Object.keys(labels)
    .sort()
    .map((k) => `${k}=${labels[k]}`)
    .join(',')
  return `${name}|${sorted}`
}

interface Series {
  name: string
  labels: Labels
  value: number
}

export class MetricsRegistry {
  private readonly counters = new Map<string, Series>()
  private readonly help = new Map<string, { help: string; type: 'counter' | 'gauge' }>()

  describe(name: string, help: string, type: 'counter' | 'gauge'): void {
    this.help.set(name, { help, type })
  }

  increment(name: string, labels: Labels = {}, by = 1): void {
    const key = seriesKey(name, labels)
    const existing = this.counters.get(key)
    if (existing) {
      existing.value += by
      return
    }
    this.counters.set(key, { name, labels, value: by })
  }

  ensure(name: string, labels: Labels = {}): void {
    const key = seriesKey(name, labels)
    if (!this.counters.has(key)) this.counters.set(key, { name, labels, value: 0 })
  }

  snapshot(): Series[] {
    return [...this.counters.values()]
  }

  render(gauges: Series[] = []): string {
    const all = [...this.counters.values(), ...gauges]
    const byName = new Map<string, Series[]>()
    for (const series of all) {
      const list = byName.get(series.name)
      if (list) list.push(series)
      else byName.set(series.name, [series])
    }

    const lines: string[] = []
    for (const [name, seriesList] of byName) {
      const meta = this.help.get(name)
      if (meta) {
        lines.push(`# HELP ${name} ${meta.help}`)
        lines.push(`# TYPE ${name} ${meta.type}`)
      }
      for (const series of seriesList) {
        lines.push(`${name}${formatLabels(series.labels)} ${series.value}`)
      }
    }
    return `${lines.join('\n')}\n`
  }
}

export const gauge = (name: string, labels: Labels, value: number): Series => ({
  name,
  labels,
  value,
})
