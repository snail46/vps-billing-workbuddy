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

# The registry is pinned before installing so the image builds against the same
# registry as local development and CI.
COPY .npmrc ./

# The shared TypeScript configuration. `tsc` resolves `extends` relative to the
# tsconfig file that declares it, so `user-web/tsconfig.json` reaching for
# `../tsconfig.base.json` requires that file to exist inside the image. Without
# it the build aborts with TS5083 before checking a single source file, and every
# component then cascades into TS6142 ("--jsx is not set").
COPY tsconfig.base.json ./

# Workspace manifests are copied ahead of sources so the dependency layer is
# cached independently of application code.
COPY package.json package-lock.json ./
COPY shared/package.json ./shared/
COPY user-web/package.json ./user-web/
COPY admin-web/package.json ./admin-web/

# npm ci installs from the lock file, so the image cannot silently resolve a
# different dependency set than the one that was reviewed.
RUN npm ci

COPY shared ./shared
COPY user-web ./user-web

# The API origin is baked into the bundle, so it is a build argument rather than
# a runtime one. It becomes unnecessary once the reverse proxy in Phase 12 makes
# the API same-origin.
ARG VITE_API_BASE_URL=http://localhost:8080
ENV VITE_API_BASE_URL=${VITE_API_BASE_URL}

RUN npm run build --workspace user-web

# ---- runtime -----------------------------------------------------------------
FROM nginx:1.27-alpine AS runtime

# The official image renders /etc/nginx/templates/*.template into
# /etc/nginx/conf.d/ at container start, substituting only variables that are
# actually defined in the environment. That is what keeps the API origin
# per-environment instead of frozen into the image.
ENV API_ORIGIN=http://localhost:8080

COPY user-web/nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /app/user-web/dist /usr/share/nginx/html

EXPOSE 80
