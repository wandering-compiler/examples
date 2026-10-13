package business

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The ids opsPermissionIDs grants are written as numbers (the project's
// own module cannot read the ACL lock at run time), so they are checked
// here against the committed lock: a re-allocation or a rename shows up as
// a failing test instead of a 403 in production.
func TestOpsPermissionIDs_MatchTheACLLock(t *testing.T) {
	lock, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "..", "proto", "domains", "app", "w17.lock.acl.proto"))
	if err != nil {
		t.Fatalf("read the ACL lock: %v", err)
	}
	want := map[string]int32{
		"USERS_DIRECTORY_CARD_QUERY_LIST_DIRECTORY_CARDS": opsPermissionIDs["directory-reader"][0],
	}
	for name, id := range want {
		m := regexp.MustCompile(`\b` + name + ` = ([0-9]+)\b`).FindSubmatch(lock)
		if m == nil {
			t.Errorf("%s is not in the ACL lock", name)
			continue
		}
		if got := string(m[1]); got != fmt.Sprint(id) {
			t.Errorf("%s is %s in the ACL lock, opsPermissionIDs grants %d", name, got, id)
		}
	}
	if n := len(opsPermissionIDs["directory-reader"]); n != 1 {
		t.Errorf("directory-reader grants %d ids; this test checks one", n)
	}
}
