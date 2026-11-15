# README and demo recording

## Positioning

Lead with a checked result under human control. Provide separate routes for a
no-account playground, a reusable production pipeline, and custom YAML.
The opening must present creating your own pipeline and choosing a ready-made
one as equally supported paths. The README headline is **Your agents. One team.
You're in control.**, with badges and a Documentation anchor between the introduction and the long-run example.
Keep the real long-run screenshot as evidence of scale; its billion-token total
is not an onboarding expectation. Explain estimates versus subscription charges.

## Initial 90-second outline

| Time | Screen/action | Message |
|---|---|---|
| 0–8s | One playground command, dashboard opens | Try AFM without model accounts; simulation label stays visible |
| 8–18s | Scope question, select an answer | You steer the task |
| 18–30s | Two workers, switch between feeds | Parallel work with independent histories |
| 30–42s | Cost overview, per-stage costs, By agent breakdown | See which stage and agent used time and money |
| 42–55s | Charts: time → estimated cost; click a bar and a timeline job | Inspect an aggregate down to a single invocation |
| 55–68s | Budget hold, edit run limit, Save and continue | Control spending without discarding the run |
| 68–78s | Ctrl+C during report, repeat command with --dir | Recover recorded progress |
| 78–86s | Finished visual report in the feed and saved artifacts | End with an inspectable result |
| 86–90s | Production pipeline command and docs URL | Bring your own project and provider access |

Use a readable 1440×900 viewport and deliberate cursor movement. Caption every
transition; no audio should be required. Cut waiting periods and label time
compression. Keep the simulated model and illustrative prices visible. Never
present the scripted result as an AI-produced change.

Keep only a looping GIF preview in the repository. MP4 is an intermediate
encoding outside the repository, not a published artifact. Keep the existing
collaboration/restart GIFs as feature-specific clips. A separate deep dive can use the real 35-hour run to
explain reviews, escalations and agent time versus elapsed time.

## Follow-up recommendations

- Ship the playground as `afm demo` with bundled fixtures and an isolated config
  path, eliminating the repository clone and inherited global hooks.
- Add a production-pipeline setup preflight that links each missing requirement
  to its setup instructions; keep model authentication explicit.
- Publish a small real-task case study: requested change, reviewed diff, checks,
  elapsed time, agent time, estimated spend and human interventions.
- Publish the recording alongside a release that includes the playground.
  Smoke-test the scenarios on Linux as well as the recorded macOS environment.

## Recorded edit

The user requested the existing GIF's dynamic style: large short captions, a
dark backdrop, fast cuts and gentle camera motion. The intermediate edit is 65 seconds,
with a 21-second GIF preview, and uses the review scenario for five parallel rows.
Its working campaign line is **Your agents. Your rules.** The budget turn is
introduced with **The budget has entered the chat.**

Captured from AFM 3.1.4 on macOS: scope answer, parallel execution, stage cost,
agent breakdown, charts, timeline-to-job navigation, budget hold, custom 20k
limit via Save and continue, actual stop/restart, and the visual report. The
restart honestly leaves partial usage coverage for the interrupted invocation.
All shots retain a scripted-agent/simulated-price label in the edit.

Reproduction scripts: `scripts/playground-video/`. The only published video
asset is `docs/assets/afm-playground.gif`. Lossless GIF optimization keeps the
frames and timing unchanged. Public release and hosting publication remain
separate from local asset creation.
