ALTER TABLE code_review_policies
    ADD COLUMN review_instructions text NOT NULL DEFAULT '',
    ADD COLUMN automated_approval_policy text NOT NULL DEFAULT $policy$Automatically approve routine, well-tested changes when:
- the intent is clear and the change has a small, understandable scope
- there are no blocking findings
- the implementation follows established repository patterns
- the available testing evidence is appropriate for the change

Require human review when:
- the change affects authentication, billing, permissions, infrastructure, or production data
- the change introduces a new architectural pattern or crosses unclear ownership boundaries
- reviewers disagree or the risk cannot be evaluated confidently
- the intended behavior cannot be determined from the pull request and repository context$policy$;
