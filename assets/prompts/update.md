Now, take the patterns saved in "<FILEPATH>" and merge them with the current high-priority patterns provided to you. Prefer merging them into existing patterns if their meanings/concepts group well together. If this is not possible, create a new pattern.

Then, follow this exact algorithm:
1. Assign every resulting pattern to exactly one priority tier: high, medium, or low.
   This classification is an internal decision used only for section ordering and for
   the discard rule below — it must never appear in the output file.
2. Keep a maximum of <MAX_RULES> patterns in total. If you need to discard patterns to meet this
   limit, drop 'low' first, then 'medium', to preserve 'high'.
3. Update the file with the finalized result. The entire content of the file must be
   written in English.

You must strictly use the following Markdown format for the file content:

  # Project rules

  ## [Pattern Name]

  [Pattern description]

(Repeat the ## block for each pattern. Encode priority ONLY through the order of the
## blocks: high first, then medium, then low. Do NOT add any priority indication to the
file: no tier headings like ## High, no tier prefix or suffix in the pattern name like
High: or [High], no tier words inside the descriptions.)
