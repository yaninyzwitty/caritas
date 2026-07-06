-- name: ValidateMemberActiveInBranch :one
SELECT id, branch_id, status
FROM members
WHERE id = $1 AND branch_id = $2 AND status = 'active' AND is_deleted = FALSE;

-- name: MemberExistsByBranchAndNationalID :one
SELECT id, status
FROM members
WHERE branch_id = $1 AND national_id = $2 AND is_deleted = FALSE;

-- name: GetMemberByID :one
SELECT m.id, m.branch_id, m.member_number, m.national_id, m.status, m.is_deleted, m.created_at, m.updated_at,
       p.full_name, p.phone, p.email, p.address, p.date_of_birth, p.occupation, p.employer,
       p.monthly_income, p.id_document_type, p.id_document_number,
       p.next_of_kin_name, p.next_of_kin_phone, p.next_of_kin_relationship
FROM members m
LEFT JOIN member_profiles p ON p.member_id = m.id
WHERE m.id = $1 AND m.is_deleted = FALSE;

-- name: GetMemberByBranchAndNumber :one
SELECT m.id, m.branch_id, m.member_number, m.national_id, m.status, m.is_deleted, m.created_at, m.updated_at,
       p.full_name, p.phone, p.email, p.address, p.date_of_birth, p.occupation, p.employer,
       p.monthly_income, p.id_document_type, p.id_document_number,
       p.next_of_kin_name, p.next_of_kin_phone, p.next_of_kin_relationship
FROM members m
LEFT JOIN member_profiles p ON p.member_id = m.id
WHERE m.branch_id = $1 AND m.member_number = $2 AND m.is_deleted = FALSE;

-- name: ListMembersByBranchCursor :many
SELECT m.id, m.branch_id, m.member_number, m.national_id, m.status, m.is_deleted, m.created_at, m.updated_at,
       p.full_name, p.phone, p.email, p.address, p.date_of_birth, p.occupation, p.employer,
       p.monthly_income, p.id_document_type, p.id_document_number,
       p.next_of_kin_name, p.next_of_kin_phone, p.next_of_kin_relationship
FROM members m
LEFT JOIN member_profiles p ON p.member_id = m.id
WHERE m.branch_id = $1 AND m.is_deleted = FALSE
  AND ($2::BIGINT IS NULL OR m.id < $2)
ORDER BY m.id DESC
LIMIT $3;

-- name: GetNextMemberNumber :one
SELECT next_member_number
FROM member_branch_counters
WHERE branch_id = $1
FOR UPDATE;

-- name: IncrementMemberNumber :one
UPDATE member_branch_counters
SET next_member_number = next_member_number + 1, updated_at = CURRENT_TIMESTAMP
WHERE branch_id = $1
RETURNING next_member_number - 1;

-- name: RecoverMemberNumberCounter :exec
UPDATE member_branch_counters c
SET next_member_number = GREATEST(next_member_number, (SELECT COALESCE(MAX(member_number), 0) + 1 FROM members m WHERE m.branch_id = $1)),
    updated_at = CURRENT_TIMESTAMP
WHERE c.branch_id = $1;

-- name: CreateMember :one
INSERT INTO members (branch_id, member_number, national_id, status)
VALUES ($1, $2, $3, $4)
ON CONFLICT (branch_id, national_id) DO NOTHING
RETURNING id;

-- name: CreateMemberProfile :one
INSERT INTO member_profiles (member_id, full_name, phone, email, address, date_of_birth, occupation, employer,
                            monthly_income, id_document_type, id_document_number,
                            next_of_kin_name, next_of_kin_phone, next_of_kin_relationship)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING member_id;

-- name: RecordMemberStatusTransition :exec
INSERT INTO member_status_history (member_id, from_status, to_status, reason)
VALUES ($1, $2, $3, $4);

-- name: UpdateMemberStatus :one
UPDATE members
SET status = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND is_deleted = FALSE
RETURNING id, branch_id, status, updated_at;

-- name: DeactivateMember :one
UPDATE members
SET is_deleted = TRUE, status = 'closed', updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND is_deleted = FALSE
RETURNING id, branch_id, status;

-- name: UpdateMemberProfile :exec
UPDATE member_profiles
SET full_name = COALESCE($2, full_name),
    phone = COALESCE($3, phone),
    email = COALESCE($4, email),
    address = COALESCE($5, address),
    date_of_birth = COALESCE($6, date_of_birth),
    occupation = COALESCE($7, occupation),
    employer = COALESCE($8, employer),
    monthly_income = COALESCE($9, monthly_income),
    id_document_type = COALESCE($10, id_document_type),
    id_document_number = COALESCE($11, id_document_number),
    next_of_kin_name = COALESCE($12, next_of_kin_name),
    next_of_kin_phone = COALESCE($13, next_of_kin_phone),
    next_of_kin_relationship = COALESCE($14, next_of_kin_relationship),
    updated_at = CURRENT_TIMESTAMP
WHERE member_id = $1;

-- name: GetMemberStatusHistory :many
SELECT id, member_id, from_status, to_status, reason, created_at
FROM member_status_history
WHERE member_id = $1
ORDER BY created_at DESC;