import { describe, expect, it } from 'vitest'
import { formatLabels, gauge, MetricsRegistry } from './registry.js'

describe('formatLabels', () => {
  it('renders nothing for an empty set', () => {
    expect(formatLabels({})).toBe('')
  })

  it('escapes characters that would break the exposition format', () => {
    expect(formatLabels({ label: 'say "hi"' })).toBe('{label="say \\"hi\\""}')
    expect(formatLabels({ label: 'a\\b' })).toBe('{label="a\\\\b"}')
    expect(formatLabels({ label: 'two\nlines' })).toBe('{label="two\\nlines"}')
  })

  it('drops empty values rather than emitting label=""', () => {
    expect(formatLabels({ a: '1', b: '' })).toBe('{a="1"}')
  })
})

describe('MetricsRegistry', () => {
  it('accumulates a counter per label set', () => {
    const registry = new MetricsRegistry()
    registry.increment('sends_total', { session_id: 'a', outcome: 'sent' })
    registry.increment('sends_total', { session_id: 'a', outcome: 'sent' })
    registry.increment('sends_total', { session_id: 'a', outcome: 'failed' })
    registry.increment('sends_total', { session_id: 'b', outcome: 'sent' })

    const values = Object.fromEntries(
      registry.snapshot().map((s) => [`${s.labels.session_id}/${s.labels.outcome}`, s.value]),
    )
    expect(values).toEqual({ 'a/sent': 2, 'a/failed': 1, 'b/sent': 1 })
  })

  it('treats label order as irrelevant to series identity', () => {
    const registry = new MetricsRegistry()
    registry.increment('x', { a: '1', b: '2' })
    registry.increment('x', { b: '2', a: '1' })
    expect(registry.snapshot()).toHaveLength(1)
    expect(registry.snapshot()[0]!.value).toBe(2)
  })

  it('can pre-register a series at zero so dashboards read "none" not "no data"', () => {
    const registry = new MetricsRegistry()
    registry.ensure('failures_total', { code: 'send_failed' })
    expect(registry.snapshot()[0]).toMatchObject({ value: 0 })

    registry.increment('failures_total', { code: 'send_failed' })
    registry.ensure('failures_total', { code: 'send_failed' })
    expect(registry.snapshot()[0]!.value).toBe(1)
  })

  it('renders HELP and TYPE once per metric, above its series', () => {
    const registry = new MetricsRegistry()
    registry.describe('sends_total', 'Send attempts', 'counter')
    registry.increment('sends_total', { outcome: 'sent' })
    registry.increment('sends_total', { outcome: 'failed' })

    const output = registry.render()
    expect(output).toContain('# HELP sends_total Send attempts')
    expect(output).toContain('# TYPE sends_total counter')
    expect(output.match(/# TYPE sends_total/g)).toHaveLength(1)
    expect(output).toContain('sends_total{outcome="sent"} 1')
    expect(output).toContain('sends_total{outcome="failed"} 1')
  })

  it('merges scrape-time gauges with stored counters', () => {
    const registry = new MetricsRegistry()
    registry.describe('uptime_seconds', 'Uptime', 'gauge')
    registry.increment('sends_total', {})

    const output = registry.render([gauge('uptime_seconds', {}, 42)])
    expect(output).toContain('uptime_seconds 42')
    expect(output).toContain('sends_total 1')
  })

  it('ends with a newline, which the exposition format requires', () => {
    const registry = new MetricsRegistry()
    registry.increment('x', {})
    expect(registry.render().endsWith('\n')).toBe(true)
  })
})
