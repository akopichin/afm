# serve testdata

`serve.yaml` is the config of the `afm serve` host started by `serve_integration_test.go`. The projects get the
flows and fake agents of `../core-mode` (`prompt.yaml`, `prompt-repeat.yaml`), so no LLM is involved.
