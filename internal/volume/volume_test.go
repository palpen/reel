package volume

import "testing"

func TestStorageSeparationFailsClosed(t *testing.T) {
	a := Identity{UUID: "camera", Device: 1, Physical: []string{"disk1"}}
	for _, b := range []Identity{{UUID: "backup", Device: 2, Physical: []string{"disk1"}}, {UUID: "camera", Device: 2, Physical: []string{"disk2"}}, {UUID: "backup", Device: 1, Physical: []string{"disk2"}}, {UUID: "backup", Device: 2}} {
		if err := Independent(a, b); err == nil {
			t.Fatal("unresolved or shared storage accepted", b)
		}
	}
	if err := Independent(a, Identity{UUID: "backup", Device: 2, Physical: []string{"disk2"}}); err != nil {
		t.Fatal(err)
	}
}
