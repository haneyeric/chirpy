-- +goose Up
CREATE TABLE chirps(
    id UUID PRIMARY KEY,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    body TEXT UNIQUE NOT NULL,
    user_id uuid REFERENCES users(id) ON DELETE CASCADE
);
-- +goose Down
DROP TABLE chirps;
