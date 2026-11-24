# dashboard

## Requirements

### Requirement: DASH-R01 — Focus mode ends only on an explicit user action
The dashboard's stage focus mode MUST stay on until the user leaves it with the stage toolbar's
**Exit focus** button, the **F** key or the **Escape** key. A view change made by the dashboard itself
MUST NOT end it: a wait of any stage opening by itself, the resolution of the wait on screen, and a
stage failing, pausing, failing a lifecycle hook or waiting for a helper decision keep focus mode on
and show that stage view inside it, with **Feed** and **Exit focus** in the stage toolbar. After a
question is answered or a plan is approved, the next waiting item of the attention queue opens, or the
stage's Feed when the queue is empty, still in focus mode. While focus mode is on, the dashboard never
stays on Cost, Team or Board: a resolved wait that would return there opens the stage's Feed instead.
Focus mode is not kept across a page reload.

Evidence: `afm:pkg/web/dashboard/src/hooks/use-feed-focus/use-feed-focus.ts` `useFeedFocus`;
`afm:pkg/web/dashboard/src/app/App.tsx` `focusAvailable`; `afm:pkg/web/dashboard/src/app/App.feed-focus.test.tsx`.

#### Scenario: DASH-S01 — Answering a question keeps focus mode
- GIVEN focus mode is on and the Question view of stage S opened by itself while no other stage waits
- WHEN the user answers the question and S resumes running
- THEN the Feed of S is shown, focus mode is still on and the toolbar offers Exit focus

#### Scenario: DASH-S02 — Approving a plan keeps focus mode
- GIVEN focus mode is on and the Approval view of stage S is shown while no other stage waits
- WHEN the user approves the plan and S resumes running
- THEN the Feed of S is shown and focus mode is still on

#### Scenario: DASH-S03 — The next waiting item opens in focus mode
- GIVEN focus mode is on, the Question view of stage S is shown and stage T also waits for an answer
- WHEN the user answers the question of S
- THEN the Question view of T opens and focus mode is still on

#### Scenario: DASH-S04 — A wait opened over Cost returns to the Feed
- GIVEN the Question view of stage S opened by itself over Cost and the user turned focus mode on there
- WHEN the user answers the question and S resumes running
- THEN the Feed of S is shown instead of Cost and focus mode is still on

#### Scenario: DASH-S05 — Failed and Paused views open inside focus mode
- GIVEN focus mode is on in the Feed of a stage
- WHEN another stage fails or the user pauses the stage, and its Failed or Paused view opens by itself
- THEN that view is shown in focus mode with Feed and Exit focus in the toolbar, and Feed returns to the Feed in focus mode

#### Scenario: DASH-S06 — Exit focus, F and Escape end focus mode
- GIVEN focus mode is on in a stage view and no dialog, menu or drawer is open
- WHEN the user clicks Exit focus, presses Escape, or presses F
- THEN focus mode ends and the run header, the tab strip and the Stages rail are shown again

### Requirement: DASH-R02 — Focus mode is offered on every view of a stage
The stage toolbar MUST offer focus mode, and **F** MUST turn it on, on a stage's Feed and on each of its
views: Approval, Question, Paused, Failed, Hook failed, Helpers, and plan or dialog history. It MUST NOT
be offered for a helper's feed or in Team, Board and Cost, where **F** does not turn it on.

Evidence: `afm:pkg/web/dashboard/src/app/App.tsx` `focusAvailable`;
`afm:pkg/web/dashboard/src/components/workspace/WorkspaceHeader.tsx` `feedFocus`;
`afm:pkg/web/dashboard/src/app/App.feed-focus.test.tsx`.

#### Scenario: DASH-S07 — A Failed view offers focus mode and Cost does not
- GIVEN focus mode is off, the Failed view of a stage is shown, and then Cost is opened
- WHEN the user presses F on the Failed view and later presses F on Cost after leaving focus mode
- THEN the Failed view offers Focus on this view and F turns focus mode on there, while Cost offers no Focus button and F leaves focus mode off
