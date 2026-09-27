import { fileURLToPath } from 'node:url'

export const MIGRATIONS_DIR = fileURLToPath(new URL('../migrations', import.meta.url))
