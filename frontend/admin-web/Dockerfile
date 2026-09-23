# syntax=docker/dockerfile:1

# Build context is the `frontend` directory, not this one: the application
# depends on the sibling `shared` workspace, and npm needs every workspace's
# package.json to resolve the lock file.
#
# The syntax directive must be the FIRST line. BuildKit only reads it there and
# silently ignores it anywhere else, so a leading comment quietly disables it.

# ---- build -------------------------------------------------------------------
FROM node:22-alpine AS build

WORKDIR /app

COPY .npmrc ./

# The shared TypeScript configuration. `tsc` resolves `extends` relative to the
# tsconfig file that declares it, so `admin-web/tsconfig.json` reaching for
# `../tsconfig.base.json` requires that file to exist inside the image. Without
# it the build aborts with TS5083 before checking a single source file, and every
# component then cascades into TS6142 ("--jsx is not set").
COPY tsconfig.base.json ./

COPY package.json package-lock.json ./
COPY shared/package.json ./shared/
COPY user-web/package.json ./user-web/
COPY admin-web/package.json ./admin-web/

RUN npm ci

COPY shared ./shared
COPY admin-web ./admin-web

ARG VITE_API_BASE_URL=http://localhost:8080
ENV VITE_API_BASE_URL=${VITE_API_BASE_URL}

RUN npm run build --workspace admin-web

# ---- runtime -----------------------------------------------------------------
FROM nginx:1.27-alpine AS runtime

ENV API_ORIGIN=http://localhost:8080

COPY admin-web/nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /app/admin-web/dist /usr/share/nginx/html

EXPOSE 80
