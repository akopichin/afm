# Pipeline package example

A manifest v2 package: `pipeline.yaml` names the flow, the agent defaults and the files copied into the
immutable snapshot (`skills/`, `scripts/`). Nothing is installed and `.afm/config.yaml` is never edited.

```bash
# Show roles, effective models and what is missing, without starting any agent
afm pipelines inspect examples/pipeline-package --pipeline-options examples/pipeline-package/pipeline-options.example.yaml

# Run it in your project; options switch the writer to Codex
cd /path/to/your/project
afm run --pipeline /path/to/afm/examples/pipeline-package \
  --pipeline-options /path/to/afm/examples/pipeline-package/pipeline-options.example.yaml
```

The `check` stage script finds its helper through `$AFM_PIPELINE_DIR`, the snapshot directory. See
[Pipeline packages](../../docs/pipelines.md) for the full format.
