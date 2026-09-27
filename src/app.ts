import type { FastifyInstance, FastifyTypeProviderDefault, RawServerDefault } from 'fastify'
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Logger } from './logger.js'

export type AppInstance = FastifyInstance<
  RawServerDefault,
  IncomingMessage,
  ServerResponse,
  Logger,
  FastifyTypeProviderDefault
>
