package directory

import "testing"

func TestResolveDocumentUsersUsesCurrentAppAndRequestedReferences(t *testing.T) {
	pool := directoryTestPool(t)
	_, err := pool.Exec(t.Context(), `INSERT INTO accounts(id,tenant_key,app_id,open_id,name) VALUES
 ('local','tenant','app','ou_local','本应用用户'),
 ('unrequested','tenant','app','ou_unrequested','未被提及用户'),
 ('foreign','tenant','other','ou_foreign','其他应用用户')`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: pool, cfg: Config{TenantKey: "tenant", AppID: "app"}}
	names, err := s.ResolveDocumentUsers(t.Context(), []string{"ou_local", "ou_foreign", "ou_missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names["ou_local"] != "本应用用户" {
		t.Fatal(names)
	}
}
