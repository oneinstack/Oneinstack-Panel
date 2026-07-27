package input

import "testing"

func TestDatabaseInputsRejectInjectionAndUnsupportedTypes(t *testing.T) {
	for _, request := range []LibParam{
		{ID: 1, Name: "site-db", Root: "site_user", Password: "secret", Encoding: "utf8mb4"},
		{ID: 1, Name: "site_db", Root: "user'@'%", Password: "secret", Encoding: "utf8mb4"},
		{ID: 1, Name: "site_db", Root: "site_user", Password: "secret", Encoding: "unknown"},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("unsafe database request was accepted: %#v", request)
		}
	}
	valid := LibParam{
		ID: 1, Name: "site_db", Root: "site_user",
		Password: "Str0ng!'secret", Encoding: "utf8",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid database request was rejected: %v", err)
	}
	if valid.Encoding != "utf8mb4" {
		t.Fatalf("encoding was not normalized: %q", valid.Encoding)
	}

	connection := AddParam{
		Addr: "localhost", Port: "3306", Root: "root",
		Password: "secret", Type: "mysql",
	}
	if err := connection.Validate(); err != nil {
		t.Fatalf("localhost connection was rejected: %v", err)
	}
	connection.Type = "mongo"
	if err := connection.Validate(); err == nil {
		t.Fatal("unsupported database type was accepted")
	}
}
