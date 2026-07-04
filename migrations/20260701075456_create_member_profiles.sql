-- +goose Up
CREATE TABLE member_profiles (
    member_id BIGINT PRIMARY KEY,
    full_name TEXT NOT NULL,
    phone TEXT NOT NULL,
    email TEXT NOT NULL,
    address TEXT,
    date_of_birth DATE,
    occupation TEXT,
    employer TEXT,
    monthly_income NUMERIC(12, 2),
    id_document_type TEXT,
    id_document_number TEXT,
    next_of_kin_name TEXT,
    next_of_kin_phone TEXT,
    next_of_kin_relationship TEXT,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT member_profiles_member_id_fkey FOREIGN KEY (member_id) REFERENCES members(id)
);

-- +goose Down
DROP TABLE IF EXISTS member_profiles;