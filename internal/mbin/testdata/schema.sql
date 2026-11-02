-- The part of Mbin's schema the digest may read, and nothing else: exactly the
-- tables and columns in Allowlist, with Mbin's types. A query that reaches
-- for anything outside the allowlist fails here before it fails at apply.

-- Mbin's own enum type for application_status (migrations/Version20241104162329.php).
CREATE TYPE enumApplicationStatus AS ENUM ('Approved', 'Rejected', 'Pending');

CREATE TABLE "user" (
    id                     INT PRIMARY KEY,
    username               VARCHAR(255) NOT NULL,
    email                  VARCHAR(255),
    is_verified            BOOLEAN NOT NULL DEFAULT false,
    is_banned              BOOLEAN NOT NULL DEFAULT false,
    is_deleted             BOOLEAN NOT NULL DEFAULT false,
    marked_for_deletion_at TIMESTAMPTZ,
    visibility             TEXT NOT NULL DEFAULT 'visible',
    type                   TEXT NOT NULL DEFAULT 'Person',
    ap_id                  VARCHAR(255),
    oauth_oidc_id          VARCHAR(255),
    last_active            TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL,
    application_status     enumApplicationStatus NOT NULL DEFAULT 'Approved'
);

CREATE TABLE magazine (
    id                     INT PRIMARY KEY,
    name                   VARCHAR(255) NOT NULL,
    title                  VARCHAR(255) NOT NULL,
    ap_id                  VARCHAR(255),
    is_adult               BOOLEAN NOT NULL DEFAULT false,
    visibility             TEXT NOT NULL DEFAULT 'visible',
    marked_for_deletion_at TIMESTAMPTZ,
    ap_deleted_at          TIMESTAMPTZ
);

CREATE TABLE entry (
    id            INT PRIMARY KEY,
    user_id       INT NOT NULL,
    magazine_id   INT NOT NULL,
    slug          VARCHAR(255),
    title         VARCHAR(255) NOT NULL,
    comment_count INT NOT NULL DEFAULT 0,
    score         INT NOT NULL DEFAULT 0,
    is_adult      BOOLEAN NOT NULL DEFAULT false,
    visibility    TEXT NOT NULL DEFAULT 'visible',
    ap_id         VARCHAR(255),
    created_at    TIMESTAMPTZ NOT NULL,
    last_active   TIMESTAMPTZ NOT NULL
);

CREATE TABLE entry_comment (
    id              INT PRIMARY KEY,
    user_id         INT NOT NULL,
    entry_id        INT NOT NULL,
    magazine_id     INT NOT NULL,
    body            TEXT,
    up_votes        INT NOT NULL DEFAULT 0,
    favourite_count INT NOT NULL DEFAULT 0,
    visibility      TEXT NOT NULL DEFAULT 'visible',
    is_adult        BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL
);

CREATE TABLE post (
    id            INT PRIMARY KEY,
    user_id       INT NOT NULL,
    magazine_id   INT NOT NULL,
    slug          VARCHAR(255),
    body          TEXT,
    comment_count INT NOT NULL DEFAULT 0,
    score         INT NOT NULL DEFAULT 0,
    is_adult      BOOLEAN NOT NULL DEFAULT false,
    visibility    TEXT NOT NULL DEFAULT 'visible',
    ap_id         VARCHAR(255),
    created_at    TIMESTAMPTZ NOT NULL,
    last_active   TIMESTAMPTZ NOT NULL
);

CREATE TABLE post_comment (
    id              INT PRIMARY KEY,
    user_id         INT NOT NULL,
    post_id         INT NOT NULL,
    magazine_id     INT NOT NULL,
    body            TEXT,
    up_votes        INT NOT NULL DEFAULT 0,
    favourite_count INT NOT NULL DEFAULT 0,
    visibility      TEXT NOT NULL DEFAULT 'visible',
    is_adult        BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL
);

CREATE TABLE entry_vote (
    user_id    INT NOT NULL,
    entry_id   INT NOT NULL,
    choice     INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE post_vote (
    user_id    INT NOT NULL,
    post_id    INT NOT NULL,
    choice     INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One table for every kind of favourite; the columns that do not apply are null.
CREATE TABLE favourite (
    user_id  INT NOT NULL,
    entry_id INT,
    post_id  INT
);

CREATE TABLE bookmark (
    user_id  INT NOT NULL,
    entry_id INT,
    post_id  INT
);

CREATE TABLE notification (
    user_id          INT NOT NULL,
    status           TEXT NOT NULL,
    entry_id         INT,
    entry_comment_id INT,
    post_id          INT,
    post_comment_id  INT
);

CREATE TABLE magazine_subscription (
    user_id     INT NOT NULL,
    magazine_id INT NOT NULL
);

CREATE TABLE magazine_block (
    user_id     INT NOT NULL,
    magazine_id INT NOT NULL
);

CREATE TABLE magazine_ban (
    user_id     INT NOT NULL,
    magazine_id INT NOT NULL,
    expired_at  TIMESTAMPTZ
);

CREATE TABLE user_block (
    blocker_id INT NOT NULL,
    blocked_id INT NOT NULL
);
