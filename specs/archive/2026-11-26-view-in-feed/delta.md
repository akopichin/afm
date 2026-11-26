# View in Feed in the Stage failed banner

## ADDED

### Requirement: dashboard/DASH-R03 — The Stage failed banner opens the failed stage's Feed
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

## MODIFIED

## REMOVED
