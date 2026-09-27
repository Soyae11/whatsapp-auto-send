import { describe, expect, it, vi } from 'vitest'
import { isolate } from './manager.js'

describe('isolate', () => {
  it('contains a synchronous throw', () => {
    const onError = vi.fn()
    expect(() =>
      isolate(() => {
        throw new Error('one session exploded')
      }, onError),
    ).not.toThrow()
    expect(onError).toHaveBeenCalledOnce()
    expect((onError.mock.calls[0]![0] as Error).message).toBe('one session exploded')
  })

  it('contains a rejected promise', async () => {
    const onError = vi.fn()
    isolate(async () => {
      throw new Error('async explosion')
    }, onError)
    await new Promise((r) => setImmediate(r))
    expect(onError).toHaveBeenCalledOnce()
  })

  it('leaves success alone', async () => {
    const onError = vi.fn()
    const ran = vi.fn()
    isolate(async () => {
      ran()
    }, onError)
    await new Promise((r) => setImmediate(r))
    expect(ran).toHaveBeenCalledOnce()
    expect(onError).not.toHaveBeenCalled()
  })

  it('keeps later work running after an earlier failure', () => {
    const errors: unknown[] = []
    const survivors: string[] = []
    for (const id of ['a', 'b', 'c']) {
      isolate(
        () => {
          if (id === 'b') throw new Error('b is broken')
          survivors.push(id)
        },
        (err) => errors.push(err),
      )
    }
    expect(survivors).toEqual(['a', 'c'])
    expect(errors).toHaveLength(1)
  })
})
