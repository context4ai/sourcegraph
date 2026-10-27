package control

import "testing"

func TestEffectiveManagersFallBackToTheCreatorUntilEdited(t *testing.T) {
	legacy := Repository{CreatorID: "u1"}
	if got := legacy.EffectiveManagers(); len(got) != 1 || got[0] != "u1" || !legacy.IsManager("u1") || legacy.IsManager("u2") {
		t.Fatalf("legacy repository managers = %v", got)
	}
	if (Repository{}).IsManager("") || len((Repository{}).EffectiveManagers()) != 0 {
		t.Fatal("service-registered repositories have no managers")
	}
	edited := Repository{CreatorID: "u1", ManagersSet: true}
	if edited.IsManager("u1") || len(edited.EffectiveManagers()) != 0 {
		t.Fatal("an emptied manager list must not fall back to the creator")
	}
	edited.Managers = []string{"u2"}
	if !edited.IsManager("u2") || edited.IsManager("u1") {
		t.Fatal("edited managers replace the creator fallback")
	}
}
