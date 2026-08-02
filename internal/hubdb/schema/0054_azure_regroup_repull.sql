-- Azure DevOps work items now group by the categories the project's own process
-- reports, which splits a raw intake column (backlog) from a groomed one
-- (unstarted). Azure syncs are incremental, so a mirrored row would keep the
-- grouping it was stored with until the item next changed. Blanking the cursor
-- makes the next sync tick pull the whole board once and re-group every row; the
-- tick after it resumes incrementally. Azure repos only — a repo is one when its
-- mirrored issues were filed under the azure tracker source.
UPDATE issue_sync SET cursor = ''
WHERE repo IN (SELECT repo FROM issues WHERE source = 'azure');
