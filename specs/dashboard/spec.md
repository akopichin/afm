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

### Requirement: DASH-R03 — The Stage failed banner opens the failed stage's Feed
The **Stage failed** banner of a stage with status `failed` MUST offer a **View in Feed** button, also
when the stage has no plan yet. The button MUST open the Feed of the stage that the Failed view shows,
including a stage the user moved to with the attention queue's **Previous item** or **Next item**, and
select that stage in the Stages rail. The Feed MUST stay open while the stage remains in the same
failure, and focus mode MUST stay as it was. On a narrow screen the button MUST stay inside the banner.
No other banner offers the button, including **Hook failed**.

Evidence: `afm:pkg/web/dashboard/src/components/workspace/AttentionBanner.tsx` `AttentionBanner`;
`afm:pkg/web/dashboard/src/app/App.tsx` `attentionBanner`; `afm:pkg/web/dashboard/src/app/App.view-in-feed.test.tsx`.

#### Scenario: DASH-S08 — View in Feed opens the Feed of a stage that failed before a plan
- GIVEN stage S failed before it wrote a plan and its Failed view is shown
- WHEN the user clicks View in Feed in the Stage failed banner
- THEN the Feed tab is active, the stage toolbar names S and the Feed shows the events of S

#### Scenario: DASH-S09 — View in Feed follows the attention queue
- GIVEN stages S and T failed and the Failed view of T is shown
- WHEN the user moves the attention queue to S and clicks View in Feed
- THEN the Feed of S is shown, not the Feed of T

#### Scenario: DASH-S10 — The Feed stays open while the stage remains failed
- GIVEN the user opened the Feed of the failed stage S with View in Feed
- WHEN the dashboard refreshes the run status and S is still in the same failure
- THEN the Feed of S stays open and the Failed view does not open again by itself

#### Scenario: DASH-S11 — View in Feed keeps focus mode
- GIVEN focus mode is on and the Failed view of stage S is shown
- WHEN the user clicks View in Feed
- THEN the Feed of S is shown, focus mode is still on and the toolbar offers Exit Feed focus

#### Scenario: DASH-S12 — View in Feed fits the banner on a narrow screen
- GIVEN the dashboard window is 390 px wide and the Failed view of a stage is shown
- WHEN the user looks at the Stage failed banner
- THEN the View in Feed button lies fully inside the banner and the page does not scroll horizontally

#### Scenario: DASH-S13 — The Hook failed banner offers no View in Feed
- GIVEN the script_before hook of stage S failed and its Hook failed view is shown
- WHEN the user looks at the Hook failed banner
- THEN the banner shows no View in Feed button
