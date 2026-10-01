package share

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	sharev1 "github.com/yaninyzwitty/caritas-backend/gen/share/v1"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type accountListQueries struct {
	sharesqlc.Querier
	list func(sharesqlc.ListShareAccountsParams) ([]sharesqlc.ListShareAccountsRow, error)
}

func (q accountListQueries) ListShareAccounts(_ context.Context, p sharesqlc.ListShareAccountsParams) ([]sharesqlc.ListShareAccountsRow, error) {
	return q.list(p)
}

func TestListShareAccountsPagination(t *testing.T) {
	id, _ := stringToUUID("00000000-0000-0000-0000-000000000010")
	calls := 0
	h := NewHandlers(nil, &Store{Querier: accountListQueries{list: func(p sharesqlc.ListShareAccountsParams) ([]sharesqlc.ListShareAccountsRow, error) {
		calls++
		if p.BranchID != 2 || p.FetchLimit != 2 || !p.StatusFilter.Valid || p.StatusFilter.ShareAccountStatus != sharesqlc.ShareAccountStatusActive {
			t.Fatalf("unexpected filters: %+v", p)
		}
		if calls == 1 {
			if p.CursorMemberNumber.Valid || p.CursorID.Valid {
				t.Fatal("first page must have no cursor")
			}
			return []sharesqlc.ListShareAccountsRow{{ID: id, MemberNumber: 10}, {MemberNumber: 11}}, nil
		}
		if p.CursorMemberNumber != (pgtype.Int8{Int64: 10, Valid: true}) || p.CursorID != id {
			t.Fatalf("cursor must use last returned account, not lookahead: %+v", p)
		}
		return []sharesqlc.ListShareAccountsRow{{MemberNumber: 11}}, nil
	}}})
	req := &sharev1.ListShareAccountsRequest{BranchId: 2, PageSize: 1, StatusFilter: sharev1.ShareAccountStatus_SHARE_ACCOUNT_STATUS_ACTIVE}
	first, err := h.ListShareAccounts(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Accounts) != 1 || first.Accounts[0].MemberNumber != 10 || first.NextPageToken == "" {
		t.Fatalf("unexpected first page: %v", first)
	}
	req.PageToken = first.NextPageToken
	last, err := h.ListShareAccounts(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Accounts) != 1 || last.Accounts[0].MemberNumber != 11 || last.NextPageToken != "" {
		t.Fatalf("unexpected final page: %v", last)
	}
}

func TestListShareAccountsRejectsInvalidCursor(t *testing.T) {
	for _, raw := range []string{"missing-separator", "10|invalid-uuid", "10|", "2026-10-01T00:00:00Z|00000000-0000-0000-0000-000000000010"} {
		t.Run(raw, func(t *testing.T) {
			h := NewHandlers(nil, nil)
			_, err := h.ListShareAccounts(context.Background(), &sharev1.ListShareAccountsRequest{
				PageToken: base64.RawURLEncoding.EncodeToString([]byte(raw)),
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %v", err)
			}
		})
	}
}
