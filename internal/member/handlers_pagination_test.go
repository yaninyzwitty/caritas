package member

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	memberv1 "github.com/yaninyzwitty/caritas-backend/gen/member/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type paginationStore struct {
	Transactor
	rows   []sqlc.ListMembersByBranchCursorRow
	params sqlc.ListMembersByBranchCursorParams
}

func (s *paginationStore) ListMembersByBranchCursor(_ context.Context, params sqlc.ListMembersByBranchCursorParams) ([]sqlc.ListMembersByBranchCursorRow, error) {
	s.params = params
	return s.rows, nil
}

func TestListMembersPagination(t *testing.T) {
	for _, tc := range []struct {
		name      string
		numbers   []int64
		token     string
		wantToken string
		wantCount int
	}{
		{"extra row", []int64{105, 104, 103}, "", "104", 2},
		{"full final page", []int64{103, 102}, "104", "", 2},
		{"partial final page", []int64{101}, "102", "", 1},
		{"empty page", nil, "101", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &paginationStore{}
			for _, number := range tc.numbers {
				store.rows = append(store.rows, sqlc.ListMembersByBranchCursorRow{MemberNumber: number})
			}
			h := NewHandlers(NewService(&Store{Querier: store}), nil)
			resp, err := h.ListMembers(context.Background(), &memberv1.ListMembersRequest{
				BranchId: 7, PageSize: 2, PageToken: tc.token, Search: " Alice ", StatusFilter: memberv1.MemberStatus_MEMBER_STATUS_ACTIVE,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Members) != tc.wantCount || resp.NextPageToken != tc.wantToken {
				t.Fatalf("got %d members, token %q; want %d, %q", len(resp.Members), resp.NextPageToken, tc.wantCount, tc.wantToken)
			}
			if store.params.FetchLimit != 3 || store.params.BranchID != 7 || store.params.Search != "Alice" || store.params.StatusFilter != "active" {
				t.Fatalf("unexpected query parameters: %+v", store.params)
			}
			var wantCursor pgtype.Int8
			if tc.token != "" {
				if err := wantCursor.Scan(tc.token); err != nil {
					t.Fatal(err)
				}
			}
			if store.params.BeforeMemberNumber != wantCursor {
				t.Fatalf("unexpected cursor: %+v", store.params.BeforeMemberNumber)
			}
		})
	}
}

func TestListMembersInvalidPageToken(t *testing.T) {
	for _, token := range []string{"0", "-1", "abc", "9223372036854775808", "ffffffff-ffff-ffff-ffff-ffffffffffff"} {
		t.Run(token, func(t *testing.T) {
			_, err := (&Handlers{}).ListMembers(context.Background(), &memberv1.ListMembersRequest{PageToken: token})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("got %v, want InvalidArgument", err)
			}
		})
	}
}
