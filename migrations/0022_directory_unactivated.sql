ALTER TABLE directory_people
    ADD COLUMN unactivated boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT directory_people_unactivated_check CHECK (NOT unactivated OR directory_status = 'inactive');
