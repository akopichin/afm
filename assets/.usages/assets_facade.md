Domain: reading embedded system prompts and installing bundled claude skills. Audience: `cmd/afm`.

## Loading a prompt template

```go
tmpl, err := assets.ReadPrompt("planning.md", cfg.PromptsDir)
// cfg.PromptsDir empty => read from the embedded FS; non-empty => read from that directory instead
```

## Installing skills

```go
fsys := assets.SkillsFS // claude/skills/afm* — 5 directories (package var, not a function call)
// installSkills copies these into the target directory; per-file, skips existing files unless
// force=true (never deletes/recreates the destination directory)
```

`assets.FS` (a package variable of type embed.FS, not a function call) exposes the raw prompts
embed.FS directly for callers that need to enumerate prompt files rather than read one by name.
