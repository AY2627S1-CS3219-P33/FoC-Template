# AI Assistance Disclosure:
# Tool: Claude, date: 2026-11-11
# Scope: frontend container image build configuration for user-frontend.
# Author review: I validated correctness, edited for style, and added boundary checks.

FROM node:22-alpine AS build

WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 80
