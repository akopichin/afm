#!/bin/sh
# Fake autonomous agent: a single implementation phase, no plan.
AFM_IT_SINGLE_PHASE=1 exec "$(dirname "$0")/agent.sh"
