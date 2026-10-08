package auth

import "testing"

// TestContributionChargePermission prevents the new assessment write RPC from
// falling through to read access, which would let auditors create obligations.
func TestContributionChargePermission(t *testing.T) {
	permission := permissionForMethod("/contribution.v1.ContributionService/CreateContributionCharge")
	if permission != permissionCashRecord {
		t.Fatalf("charge creation permission = %q", permission)
	}
	for _, role := range []string{roleCashier, roleManager, roleSystemAdmin} {
		if !roleHasPermission(role, permission) {
			t.Fatalf("%s cannot create a charge", role)
		}
	}
	if roleHasPermission(roleAuditor, permission) {
		t.Fatal("auditor can create a charge")
	}
}
