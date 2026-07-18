package schedule

import "testing"

func TestCatalogContract(t *testing.T) {
	if got, want := ToolCatalogVersion, "s06-schedule-v4"; got != want {
		t.Fatalf("ToolCatalogVersion = %q, want %q", got, want)
	}

	got := DynamicToolNames()
	want := []string{"create", "list_own", "update"}
	if len(got) != len(want) {
		t.Fatalf("DynamicToolNames() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DynamicToolNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestOwnerScopeRequiresAppChannelAndActor(t *testing.T) {
	owner := Owner{AppID: "app-1", ChatGroupID: "group-1", OpenID: "user-1"}
	if err := owner.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for _, owner := range []Owner{
		{ChatGroupID: "group-1", OpenID: "user-1"},
		{AppID: "app-1", OpenID: "user-1"},
		{AppID: "app-1", ChatGroupID: "group-1"},
	} {
		if err := owner.Validate(); err == nil {
			t.Fatalf("Validate() = nil for incomplete owner %#v", owner)
		}
	}
}
