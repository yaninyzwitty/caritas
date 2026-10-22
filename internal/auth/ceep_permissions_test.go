package auth

import "testing"

// TestCEEPFinalizationPermission protects the report's write boundary; removing
// this regression check could let a newly added RPC fall through to read access.
func TestCEEPFinalizationPermission(t *testing.T) {
	permission := permissionForMethod("/contribution.v1.ContributionService/FinalizeCEEPReport")
	if permission != permissionCashApprove {
		t.Fatalf("finalization permission = %q", permission)
	}
	for _, role := range []string{roleManager, roleSystemAdmin} {
		if !roleHasPermission(role, permission) {
			t.Fatalf("%s cannot finalize", role)
		}
	}
	for _, role := range []string{roleCashier, roleAuditor, roleLoanOfficer, roleSecretary, roleChairperson} {
		if roleHasPermission(role, permission) {
			t.Fatalf("%s can finalize without cash approval", role)
		}
	}
	if permissionForMethod("/contribution.v1.ContributionService/GetCEEPReport") != "" {
		t.Fatal("authenticated branch readers cannot read CEEP reports")
	}
}
