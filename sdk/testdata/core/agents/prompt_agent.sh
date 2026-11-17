#!/bin/sh
# Fake autonomous agent that appends the prompt it receives on stdin to AFM_IT_PROMPT_LOG.
# The record is assembled first and written once, ending with a marker so readers skip partial records.
prompt=$(cat)
printf '### %s\n%s\n--- end ---\n' "$AFM_STAGE_DIR" "$prompt" >> "$AFM_IT_PROMPT_LOG"
AFM_IT_SINGLE_PHASE=1 exec "$(dirname "$0")/agent.sh"
