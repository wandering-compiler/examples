# The reference example's frontend: built once, then served as static files by
# nginx. Never a dev server in an image — `npm run dev` is for a laptop.
#
# Build context = the project root (it needs w17/web-client beside web/):
#   docker build -f web/Dockerfile --build-arg VITE_API_URL=https://app.api.example.test .
#
# VITE_API_URL is the API the bundle calls, baked in at build time: the deploy
# passes ${surfaces.app-api.url} (w17/infra.yaml), the local stack the
# gateway's published port (compose.reference.yaml).

FROM node:22-alpine AS build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY w17/web-client /src/w17/web-client
COPY web/ ./
ARG VITE_API_URL
RUN test -n "$VITE_API_URL" || { echo "VITE_API_URL is required (the API the bundle calls)"; exit 1; }
RUN VITE_API_URL="$VITE_API_URL" npm run build

FROM nginx:1.27-alpine
COPY web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/web/dist /usr/share/nginx/html
EXPOSE 8080
