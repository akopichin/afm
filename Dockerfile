FROM registry.ae-rus.net/mirror/golang/golang1.26 AS builder
ARG CI_USER=gitlab-ci-token
ARG CI_TOKEN
ARG BRANCH_NAME
ARG COMMIT_SHA

WORKDIR /go/src/app

COPY . .

RUN git config --global credential.helper store \
    && echo "https://${CI_USER}:${CI_TOKEN}@gitlab.ae-rus.net" >> ~/.git-credentials \
    && GIT_BRANCH=${BRANCH_NAME} GIT_HASH=${COMMIT_SHA} make build

FROM registry.ae-rus.net/mirror/debian:bookworm-slim

ENV USER=appuser
RUN addgroup --system ${USER} && adduser --system --group ${USER} --uid 1001
USER ${USER}

WORKDIR /app

COPY --chown=${USER}:${USER} --from=builder /go/src/app/bin/flowmanager .

CMD ["./flowmanager"]
