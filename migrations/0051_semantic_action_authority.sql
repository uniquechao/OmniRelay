-- Existing receipts remain recoverable, but an in-flight historical command
-- cannot acquire fresh grants. Only newly admitted authenticated chat is planned.
ALTER TABLE agent_group_work ADD COLUMN requested_intent text NOT NULL DEFAULT '',
 ADD COLUMN semantic_plan jsonb,
 ADD COLUMN needs_plan boolean NOT NULL DEFAULT false,
 ADD COLUMN proposal_items jsonb,
 ADD COLUMN proposal_parent text REFERENCES agent_group_work(id),
 ADD COLUMN confirmation_bound boolean NOT NULL DEFAULT false,
 ADD COLUMN confirmation_origin text REFERENCES agent_group_work(id),
 ADD COLUMN confirmation_item integer CHECK (confirmation_item BETWEEN 1 AND 32),
 ADD CHECK ((confirmation_origin IS NULL)=(confirmation_item IS NULL));
UPDATE agent_group_work SET requested_intent=intent;
CREATE TABLE agent_action_grants_used (
 work_id text NOT NULL REFERENCES agent_group_work(id) ON DELETE CASCADE,
 item integer NOT NULL CHECK(item BETWEEN 1 AND 32),
 slot integer NOT NULL CHECK(slot BETWEEN 1 AND 128),
 origin_work text REFERENCES agent_group_work(id),
 origin_item integer,
 PRIMARY KEY(work_id,item), UNIQUE(work_id,slot), UNIQUE(origin_work,origin_item),
 CHECK ((origin_work IS NULL)=(origin_item IS NULL))
);
