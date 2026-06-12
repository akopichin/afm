FROM golang:1.26 AS builder
ARG BRANCH_NAME
ARG COMMIT_SHA

WORKDIR /go/src/app

COPY . .

RUN GIT_BRANCH=${BRANCH_NAME} GIT_HASH=${COMMIT_SHA} make build

FROM debian:bookworm-slim

ENV USER=appuser
RUN addgroup --system ${USER} && adduser --system --group ${USER} --uid 1001
USER ${USER}

WORKDIR /app

COPY --chown=${USER}:${USER} --from=builder /go/src/app/bin/flowmanager .

CMD ["./flowmanager"]
