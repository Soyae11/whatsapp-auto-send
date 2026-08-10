import { BufferJSON } from 'baileys'

export function serialise(value: unknown): string {
  return JSON.stringify(value, BufferJSON.replacer)
}

export function deserialise<T>(json: string): T {
  return JSON.parse(json, BufferJSON.reviver) as T
}
