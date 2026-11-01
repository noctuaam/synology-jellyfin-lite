package main

import "testing"

func TestCompleteOfflineInstallationVerification(t *testing.T) {
	s, _ := fixture(t)
	if err := s.verifyCatalog(); err != nil {
		t.Fatal(err)
	}
	var temporary int
	if err := s.db.QueryRow("SELECT count(*) FROM account_tokens").Scan(&temporary); err != nil || temporary != 0 {
		t.Fatal("verification left a temporary login", err)
	}
}

func TestHTTPNeverInheritsAnInternalAdministratorView(t *testing.T) {
	s, _ := fixture(t)
	administrator, ok := s.accountView(userID)
	if !ok {
		t.Fatal("missing administrator")
	}
	for _, path := range []string{"/Items", "/NAS/Users", "/NAS/Settings"} {
		if response := accountRequest(administrator, "GET", path, "", ""); response.Code != 401 {
			t.Fatalf("%s: anonymous request inherited an administrator: %d", path, response.Code)
		}
	}
}
