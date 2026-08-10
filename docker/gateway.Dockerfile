# wa-gateway (../baileys). Built with `context: ./baileys`, so every path below is
# relative to that directory and ./baileys/.dockerignore still applies.
# syntax=docker/dockerfile:1
FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:22-alpine AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY package.json package-lock.json tsconfig.json tsconfig.build.json ./
COPY src ./src
RUN npm run build

FROM node:22-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production
COPY package.json package-lock.json ./
RUN npm ci --omit=dev && npm cache clean --force
COPY --from=build /app/dist ./dist
# Migrations are read at runtime, not baked into the bundle.
COPY migrations ./migrations

USER node
EXPOSE 3000
CMD ["node", "dist/index.js"]
