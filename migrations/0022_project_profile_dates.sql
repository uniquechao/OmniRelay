ALTER TABLE project_profiles
    ADD COLUMN planned_start date,
    ADD COLUMN planned_end date,
    ADD CONSTRAINT project_profile_plan_order CHECK (planned_start <= planned_end);
