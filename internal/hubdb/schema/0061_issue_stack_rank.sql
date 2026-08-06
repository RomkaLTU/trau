-- The board's own drag order (Microsoft.VSTS.Common.StackRank), which an Azure
-- DevOps mirror sorts by inside each state-group section. Azure DevOps only: every
-- other provider leaves it NULL, and so does a work item whose type carries no
-- Stack Rank — NULL is what sorts those last rather than first.
--
-- Azure syncs are incremental, so a mirrored row would carry no rank until the work
-- item next changed. Blanking the cursor makes the next tick pull the whole board
-- once and fill every rank; the tick after it resumes incrementally. A repo is an
-- Azure one when its mirrored issues were filed under the azure tracker source.
ALTER TABLE issues ADD COLUMN stack_rank REAL;

UPDATE issue_sync SET cursor = ''
WHERE repo IN (SELECT repo FROM issues WHERE source = 'azure');
