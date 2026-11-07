Prioritize the patterns into High, Medium, and Low tiers. Every pattern must be assigned to exactly one tier. Crucially, all three tiers (High, Medium, and Low) MUST be utilized, meaning no tier can be left empty.

Priority rule — strong bias toward user input. A pattern whose line is prefixed with `[user]` is derived from direct user input; by default it goes to the High tier. A pattern with no `[user]` prefix is derived only from logs; by default it goes to Medium or Low. The only exception: a log-only pattern that is critically important to the project (a core architectural or global-correctness rule) may be promoted to High when it clearly outweighs the user-derived patterns — but keep the tilt toward user input, do not promote log-only patterns lightly.

Strip the `[user]` prefix from your output: the tier sections must contain clean lines `N. Name — description` with no provenance marker.

Output exactly three Markdown sections, in this order, using these exact headings and nothing else before the first heading:

## High
<numbered list of High patterns: "N. Name — description">

## Medium
<numbered list of Medium patterns>

## Low
<numbered list of Low patterns>
