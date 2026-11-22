FROM node:24-bookworm-slim
WORKDIR /opt/remote-acp
RUN npm install --omit=dev --no-audit --no-fund ws@8.23.0 @agentclientprotocol/claude-agent-acp@0.90.0
COPY bridge.mjs .
RUN mkdir /workspace && chown node:node /workspace
ENV PATH=/opt/remote-acp/node_modules/.bin:$PATH ACP_LISTEN=0.0.0.0
USER node
EXPOSE 8765
CMD ["node", "bridge.mjs"]
