-- Business questions from group mentions have a private workbench result and a separate public receipt.
ALTER TABLE agent_group_work ADD COLUMN group_question text,
    ADD COLUMN group_asked_at timestamptz, ADD COLUMN group_answer text,
    ADD CHECK ((group_question IS NULL) = (group_asked_at IS NULL)),
    ADD CHECK (group_question IS NULL OR mode='group_reply'),
    ADD CHECK (group_answer IS NULL OR group_question IS NOT NULL);

-- Mutations and their acknowledgement commit with the authorized work transaction.
CREATE TABLE agent_project_tool_calls (
    work_id text NOT NULL REFERENCES agent_group_work(id) ON DELETE CASCADE,
    slot integer NOT NULL CHECK (slot BETWEEN 1 AND 128),
    tool text NOT NULL,
    fingerprint text NOT NULL,
    receipt jsonb NOT NULL,
    PRIMARY KEY(work_id, slot)
);
-- A restricted read is re-evaluated before publication or history display.
CREATE TABLE agent_project_tool_reads (
    work_id text NOT NULL REFERENCES agent_group_work(id) ON DELETE CASCADE,
    tool text NOT NULL,
    fingerprint text NOT NULL,
    args jsonb NOT NULL,
    digest text NOT NULL,
    PRIMARY KEY(work_id, tool, fingerprint)
);
