#!/bin/sh
# Fake helper: answers the user in the mesh at once and finishes with a passing verdict.
"${AFM_IT_BIN:-afm}" agent send --to user --kind message --body "helper says: looks fine" > /dev/null
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"{\"schema_version\":1,\"kind\":\"result\",\"verdict\":\"pass\",\"summary\":\"looks fine\"}"}]}}'
echo '{"type":"result","subtype":"success"}'
